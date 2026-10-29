import { Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import {
  AckPolicy,
  connect,
  JetStreamClient,
  JetStreamManager,
  nanos,
  NatsConnection,
  RetentionPolicy,
  StorageType,
} from 'nats';
import { QueueWorker } from '../queue-worker';
import {
  QueueDelivery,
  QueueSubscription,
  QueueTransport,
} from '../queue.types';

export class NatsTransport implements QueueTransport {
  private readonly logger = new Logger(NatsTransport.name);
  private connection?: NatsConnection;
  private client?: JetStreamClient;
  private manager?: JetStreamManager;
  private readonly stream: string;
  private readonly group: string;
  private readonly leaseMs: number;

  constructor(private readonly config: ConfigService) {
    this.stream = config.getOrThrow<string>('queue.natsStream');
    this.group = config.getOrThrow<string>('queue.consumerGroup');
    this.leaseMs = config.getOrThrow<number>('queue.leaseMs');
  }

  async connect(): Promise<void> {
    this.connection = await connect({
      servers: this.config.getOrThrow<string>('queue.natsUrl'),
      name: 'eitri-whatsapp-gateway',
      maxReconnectAttempts: -1,
    });
    this.client = this.connection.jetstream();
    this.manager = await this.connection.jetstreamManager();
    const topics = Object.values(
      this.config.getOrThrow<Record<string, string>>('queue.topics'),
    );
    const subjects = [...topics, ...topics.map((topic) => `${topic}.dead`)];
    const duplicateWindow = nanos(
      this.config.getOrThrow<number>('queue.dedupSeconds') * 1000,
    );
    try {
      const existing = await this.manager.streams.info(this.stream);
      await this.manager.streams.update(this.stream, {
        subjects: [...new Set([...existing.config.subjects, ...subjects])],
        duplicate_window: duplicateWindow,
      });
    } catch (error) {
      if ((error as { code?: string }).code !== '404') throw error;
      await this.manager.streams.add({
        name: this.stream,
        subjects,
        storage: StorageType.File,
        retention: RetentionPolicy.Workqueue,
        duplicate_window: duplicateWindow,
      });
    }
  }

  async publish(topic: string, body: string, messageId: string): Promise<void> {
    if (!this.client) throw new Error('NATS is not connected');
    await this.client.publish(topic, Buffer.from(body), {
      msgID: `${topic}:${messageId}`,
    });
  }

  async subscribe(
    topic: string,
    handler: (delivery: QueueDelivery) => Promise<void>,
  ): Promise<QueueSubscription> {
    if (!this.client || !this.manager) throw new Error('NATS is not connected');
    const durable = `${this.group}_${topic}`.replace(/[^a-zA-Z0-9_-]/g, '_');
    try {
      await this.manager.consumers.info(this.stream, durable);
    } catch (error) {
      if ((error as { code?: string }).code !== '404') throw error;
      await this.manager.consumers.add(this.stream, {
        durable_name: durable,
        filter_subject: topic,
        ack_policy: AckPolicy.Explicit,
        ack_wait: nanos(this.leaseMs),
        max_ack_pending: 100,
      });
    }
    const consumer = await this.client.consumers.get(this.stream, durable);
    return new QueueWorker(async () => {
      const message = await consumer.next({ expires: 1000 });
      if (!message) return;
      const heartbeat = setInterval(
        () => message.working(),
        Math.floor(this.leaseMs / 3),
      );
      try {
        await handler({
          id: String(message.info.streamSequence),
          body: message.string(),
          attempt: message.info.deliveryCount,
        });
        await message.ackAck();
      } catch (error) {
        message.nak(this.leaseMs);
        throw error;
      } finally {
        clearInterval(heartbeat);
      }
    }, this.logger);
  }

  async check(): Promise<void> {
    if (!this.connection || this.connection.isClosed())
      throw new Error('NATS is not connected');
    await this.connection.flush();
    await this.manager?.streams.info(this.stream);
  }

  async close(): Promise<void> {
    await this.connection?.drain();
  }
}
