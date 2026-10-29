export type QueueProvider = 'redis' | 'postgres' | 'nats';

export interface QueueDelivery {
  id: string;
  body: string;
  attempt: number;
}

export interface QueueSubscription {
  stop(): Promise<void>;
}

export interface QueueTransport {
  connect(): Promise<void>;
  publish(topic: string, body: string, messageId: string): Promise<void>;
  subscribe(
    topic: string,
    handler: (delivery: QueueDelivery) => Promise<void>,
  ): Promise<QueueSubscription>;
  check(): Promise<void>;
  close(): Promise<void>;
}

export class InvalidQueueMessage extends Error {}
