import {
  Injectable,
  Logger,
  OnModuleInit,
  OnApplicationShutdown,
} from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { randomUUID } from 'node:crypto';
import { event, queueTelemetry } from '../../common/utils/telemetry';
import { createQueueTransport } from './transports/factory';
import { PermanentDeliveryError } from '../../common/errors/permanent-delivery.error';
import {
  InvalidQueueMessage,
  QueueProvider,
  QueueSubscription,
  QueueTransport,
} from './queue.types';

@Injectable()
export class QueueService implements OnModuleInit, OnApplicationShutdown {
  private readonly logger = new Logger(QueueService.name);
  private readonly transport: QueueTransport;
  private readonly subscriptions: QueueSubscription[] = [];
  private connection?: Promise<void>;
  readonly provider: QueueProvider;

  constructor(private readonly config: ConfigService) {
    this.provider = config.getOrThrow<QueueProvider>('queue.provider');
    const topics = Object.values(
      config.getOrThrow<Record<string, string>>('queue.topics'),
    );
    if (new Set(topics).size !== topics.length)
      throw new Error('Queue topics must be unique');
    this.transport = createQueueTransport(this.provider, config);
  }

  async onModuleInit(): Promise<void> {
    this.connection ??= this.transport.connect();
    await this.connection;
  }

  async publish(
    topic: string,
    payload: unknown,
    messageId: string = randomUUID(),
  ): Promise<void> {
    await this.onModuleInit();
    await this.transport.publish(topic, JSON.stringify(payload), messageId);
    event(this.logger, 'queue.published', {
      ...queueTelemetry(payload),
      topic,
      queue_message_id: messageId,
    });
  }

  async subscribe(
    topic: string,
    handler: (payload: unknown) => Promise<void>,
  ): Promise<void> {
    await this.onModuleInit();
    const maxAttempts = this.config.getOrThrow<number>('queue.maxAttempts');
    const subscription = await this.transport.subscribe(
      topic,
      async (delivery) => {
        let payload: unknown;
        try {
          try {
            payload = JSON.parse(delivery.body);
          } catch {
            throw new InvalidQueueMessage('Invalid JSON');
          }
          const started = Date.now();
          event(this.logger, 'queue.processing', {
            ...queueTelemetry(payload),
            topic,
            delivery_id: delivery.id,
            attempt: delivery.attempt,
          });
          await handler(payload);
          event(this.logger, 'queue.handled', {
            ...queueTelemetry(payload),
            topic,
            delivery_id: delivery.id,
            attempt: delivery.attempt,
            duration_ms: Date.now() - started,
          });
        } catch (error) {
          const permanent =
            error instanceof InvalidQueueMessage ||
            error instanceof PermanentDeliveryError;
          if (!permanent && delivery.attempt < maxAttempts) {
            event(this.logger, 'queue.retry', {
              ...queueTelemetry(payload),
              topic,
              delivery_id: delivery.id,
              attempt: delivery.attempt,
              error_type: error instanceof Error ? error.name : 'UnknownError',
            });
            throw error;
          }
          await this.publish(
            `${topic}.dead`,
            {
              source: topic,
              messageId: delivery.id,
              body: delivery.body,
              attempts: delivery.attempt,
              reason: permanent ? error.message : 'Processing failed',
              failedAt: new Date().toISOString(),
            },
            `dead:${delivery.id}`,
          );
          event(this.logger, 'queue.dead_lettered', {
            ...queueTelemetry(payload),
            topic,
            delivery_id: delivery.id,
            attempt: delivery.attempt,
          });
        }
      },
    );
    this.subscriptions.push(subscription);
  }

  async check(): Promise<void> {
    await this.transport.check();
  }

  async onApplicationShutdown(): Promise<void> {
    await Promise.all(
      this.subscriptions.map((subscription) => subscription.stop()),
    );
    await this.transport.close();
  }
}
