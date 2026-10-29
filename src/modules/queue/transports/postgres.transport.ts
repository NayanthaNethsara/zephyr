import { Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { randomUUID } from 'node:crypto';
import { setTimeout } from 'node:timers/promises';
import { Pool } from 'pg';
import { QueueWorker } from '../queue-worker';
import {
  QueueDelivery,
  QueueSubscription,
  QueueTransport,
} from '../queue.types';

interface QueueRow {
  id: string;
  body: string;
  attempt: number;
}

export class PostgresTransport implements QueueTransport {
  private readonly logger = new Logger(PostgresTransport.name);
  private readonly pool: Pool;
  private readonly leaseMs: number;
  private readonly dedupSeconds: number;

  constructor(config: ConfigService) {
    this.pool = new Pool({
      connectionString: config.getOrThrow<string>('queue.postgresUrl'),
      max: 5,
      connectionTimeoutMillis: 5000,
      statement_timeout: 10000,
    });
    this.pool.on('error', (error) =>
      this.logger.error('Queue database connection failed', error.message),
    );
    this.leaseMs = config.getOrThrow<number>('queue.leaseMs');
    this.dedupSeconds = config.getOrThrow<number>('queue.dedupSeconds');
  }

  async connect(): Promise<void> {
    const client = await this.pool.connect();
    try {
      await client.query('BEGIN');
      await client.query('SELECT pg_advisory_xact_lock(73410821)');
      await client.query(`
      CREATE TABLE IF NOT EXISTS gateway_queue_messages (
        id uuid PRIMARY KEY,
        topic text NOT NULL,
        message_key text NOT NULL,
        body text NOT NULL,
        attempt integer NOT NULL DEFAULT 0,
        available_at timestamptz NOT NULL DEFAULT now(),
        lease_token uuid,
        completed_at timestamptz,
        created_at timestamptz NOT NULL DEFAULT now(),
        UNIQUE(topic, message_key)
      )
    `);
      await client.query(`CREATE INDEX IF NOT EXISTS gateway_queue_pending_idx
      ON gateway_queue_messages(topic, available_at, created_at) WHERE completed_at IS NULL`);
      await client.query(`CREATE INDEX IF NOT EXISTS gateway_queue_completed_idx
        ON gateway_queue_messages(completed_at) WHERE completed_at IS NOT NULL`);
      await client.query('COMMIT');
    } catch (error) {
      await client.query('ROLLBACK');
      throw error;
    } finally {
      client.release();
    }
  }

  async publish(topic: string, body: string, messageId: string): Promise<void> {
    await this.pool.query(
      `INSERT INTO gateway_queue_messages (id, topic, message_key, body)
      VALUES ($1, $2, $3, $4) ON CONFLICT(topic, message_key) DO NOTHING`,
      [randomUUID(), topic, messageId, body],
    );
  }

  async subscribe(
    topic: string,
    handler: (delivery: QueueDelivery) => Promise<void>,
  ): Promise<QueueSubscription> {
    await this.check();
    let lastCleanup = 0;
    return new QueueWorker(async () => {
      if (Date.now() - lastCleanup > 60000) {
        await this.pool.query(
          `DELETE FROM gateway_queue_messages WHERE id IN (
          SELECT id FROM gateway_queue_messages WHERE completed_at < now() - ($1 * interval '1 second') LIMIT 1000
        )`,
          [this.dedupSeconds],
        );
        lastCleanup = Date.now();
      }
      const token = randomUUID();
      const result = await this.pool.query<QueueRow>(
        `
        WITH pending AS (
          SELECT id FROM gateway_queue_messages
          WHERE topic = $1 AND completed_at IS NULL AND available_at <= now()
          ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
        ) UPDATE gateway_queue_messages AS message
          SET available_at = now() + ($2 * interval '1 millisecond'),
              lease_token = $3, attempt = attempt + 1
          FROM pending WHERE message.id = pending.id
          RETURNING message.id, message.body, message.attempt
      `,
        [topic, this.leaseMs, token],
      );
      const row = result.rows[0];
      if (!row) {
        await setTimeout(500);
        return;
      }
      const heartbeat = setInterval(
        () => {
          void this.pool
            .query(
              `UPDATE gateway_queue_messages
          SET available_at = now() + ($3 * interval '1 millisecond')
          WHERE id = $1 AND lease_token = $2 AND completed_at IS NULL`,
              [row.id, token, this.leaseMs],
            )
            .catch((error: unknown) => {
              this.logger.error(
                'PostgreSQL lease renewal failed',
                error instanceof Error ? error.message : 'Unknown error',
              );
            });
        },
        Math.floor(this.leaseMs / 3),
      );
      try {
        await handler(row);
        await this.pool.query(
          `UPDATE gateway_queue_messages SET completed_at = now(), lease_token = NULL
          WHERE id = $1 AND lease_token = $2`,
          [row.id, token],
        );
      } finally {
        clearInterval(heartbeat);
      }
    }, this.logger);
  }

  async check(): Promise<void> {
    await this.pool.query('SELECT 1');
  }

  async close(): Promise<void> {
    await this.pool.end();
  }
}
