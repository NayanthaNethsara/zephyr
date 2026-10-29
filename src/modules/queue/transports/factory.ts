import { ConfigService } from '@nestjs/config';
import { QueueProvider, QueueTransport } from '../queue.types';
import { RedisTransport } from './redis.transport';
import { PostgresTransport } from './postgres.transport';
import { NatsTransport } from './nats.transport';

export function createQueueTransport(
  provider: QueueProvider,
  config: ConfigService,
): QueueTransport {
  switch (provider) {
    case 'redis':
      return new RedisTransport(config);
    case 'postgres':
      return new PostgresTransport(config);
    case 'nats':
      return new NatsTransport(config);
  }
}
