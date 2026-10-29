import { Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { randomUUID } from 'node:crypto';
import Redis from 'ioredis';
import { QueueWorker } from '../queue-worker';
import {
  QueueDelivery,
  QueueSubscription,
  QueueTransport,
} from '../queue.types';

type StreamEntry = [string, string[]];

export class RedisTransport implements QueueTransport {
  private readonly logger = new Logger(RedisTransport.name);
  private readonly client: Redis;
  private readonly prefix: string;
  private readonly group: string;
  private readonly leaseMs: number;
  private readonly dedupSeconds: number;

  constructor(config: ConfigService) {
    this.client = new Redis(config.getOrThrow<string>('queue.redisUrl'), {
      lazyConnect: true,
      maxRetriesPerRequest: 2,
      connectTimeout: 5000,
    });
    this.client.on('error', (error) =>
      this.logger.error('Redis connection failed', error.message),
    );
    this.prefix = config.getOrThrow<string>('queue.redisPrefix');
    this.group = config.getOrThrow<string>('queue.consumerGroup');
    this.leaseMs = config.getOrThrow<number>('queue.leaseMs');
    this.dedupSeconds = config.getOrThrow<number>('queue.dedupSeconds');
  }

  async connect(): Promise<void> {
    await this.client.connect();
    await this.check();
  }

  private key(topic: string): string {
    return `${this.prefix}:{${topic}}`;
  }

  async publish(topic: string, body: string, messageId: string): Promise<void> {
    const key = this.key(topic);
    await this.client.eval(
      `
      if redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
      local id = redis.call('XADD', KEYS[1], '*', 'body', ARGV[1])
      redis.call('SET', KEYS[2], id, 'EX', ARGV[2])
      return 1
    `,
      2,
      key,
      `${key}:dedup:${messageId}`,
      body,
      this.dedupSeconds,
    );
  }

  async subscribe(
    topic: string,
    handler: (delivery: QueueDelivery) => Promise<void>,
  ): Promise<QueueSubscription> {
    const key = this.key(topic);
    const ensureGroup = async () => {
      try {
        await this.client.xgroup('CREATE', key, this.group, '0', 'MKSTREAM');
      } catch (error) {
        if (!(error instanceof Error) || !error.message.includes('BUSYGROUP'))
          throw error;
      }
    };
    await ensureGroup();

    const consumer = randomUUID();
    const reader = this.client.duplicate({ maxRetriesPerRequest: null });
    reader.on('error', (error) =>
      this.logger.error('Redis reader failed', error.message),
    );
    let cursor = '0-0';
    return new QueueWorker(
      async () => {
        let entries: StreamEntry[] = [];
        try {
          const claimed = (await reader.xautoclaim(
            key,
            this.group,
            consumer,
            this.leaseMs,
            cursor,
            'COUNT',
            1,
          )) as [string, StreamEntry[]] | null;
          if (claimed) {
            cursor = claimed[0] || '0-0';
            entries = claimed[1] ?? [];
          }
          if (!entries.length) {
            const streams = (await reader.xreadgroup(
              'GROUP',
              this.group,
              consumer,
              'COUNT',
              1,
              'BLOCK',
              1000,
              'STREAMS',
              key,
              '>',
            )) as [string, StreamEntry[]][] | null;
            entries = streams?.[0]?.[1] ?? [];
          }
        } catch (error) {
          if (error instanceof Error && error.message.includes('NOGROUP')) {
            await ensureGroup();
            return;
          }
          throw error;
        }

        for (const [id, fields] of entries) {
          if (!fields || !Array.isArray(fields)) continue;
          const pending = (await this.client.xpending(
            key,
            this.group,
            id,
            id,
            1,
          )) as [string, string, number, number][];
          const heartbeat = setInterval(
            () => {
              void this.client
                .eval(
                  `
            local pending = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[3], ARGV[3], 1)
            if #pending == 0 or pending[1][2] ~= ARGV[2] then return 0 end
            redis.call('XCLAIM', KEYS[1], ARGV[1], ARGV[2], 0, ARGV[3], 'JUSTID')
            return 1
          `,
                  1,
                  key,
                  this.group,
                  consumer,
                  id,
                )
                .catch((error: unknown) => {
                  this.logger.error(
                    'Redis lease renewal failed',
                    error instanceof Error ? error.message : 'Unknown error',
                  );
                });
            },
            Math.floor(this.leaseMs / 3),
          );
          try {
            const bodyIndex = fields.indexOf('body');
            await handler({
              id,
              body: bodyIndex < 0 ? '' : fields[bodyIndex + 1],
              attempt: pending[0]?.[3] ?? 1,
            });
            await this.client.eval(
              `
            local pending = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[3], ARGV[3], 1)
            if #pending == 0 or pending[1][2] ~= ARGV[2] then return 0 end
            redis.call('XACK', KEYS[1], ARGV[1], ARGV[3])
            redis.call('XDEL', KEYS[1], ARGV[3])
            return 1
          `,
              1,
              key,
              this.group,
              consumer,
              id,
            );
          } finally {
            clearInterval(heartbeat);
          }
        }
      },
      this.logger,
      () => reader.disconnect(),
    );
  }

  async check(): Promise<void> {
    await this.client.ping();
  }

  close(): Promise<void> {
    this.client.disconnect();
    return Promise.resolve();
  }
}
