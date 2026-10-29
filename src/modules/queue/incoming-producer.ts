import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { maskPhoneNumber } from '../../common/utils/logger.utils';
import { QueueService } from './queue.service';
import { IncomingMessageDto } from './contracts/message.dto';
import { InboxService } from '../inbox/inbox.service';
import { OutgoingProducer } from './outgoing-producer';
import { replyDeadline } from '../../lib/validation/message-age';

const TOPIC_CONFIG_KEYS: Record<IncomingMessageDto['type'], string> = {
  text: 'queue.topics.incomingText',
  image: 'queue.topics.incomingImage',
  video: 'queue.topics.incomingVideo',
  audio: 'queue.topics.incomingAudio',
  document: 'queue.topics.incomingDocument',
};

@Injectable()
export class IncomingProducer {
  private readonly logger = new Logger(IncomingProducer.name);
  private readonly topics: Record<IncomingMessageDto['type'], string>;

  constructor(
    private readonly queueService: QueueService,
    private readonly configService: ConfigService,
    private readonly inbox: InboxService,
    private readonly outgoing: OutgoingProducer,
  ) {
    this.topics = Object.fromEntries(
      Object.entries(TOPIC_CONFIG_KEYS).map(([type, key]) => {
        const topic = configService.get<string>(key) || '';
        if (!topic) {
          this.logger.warn(`queue topic for incoming ${type} not configured`);
        }
        return [type, topic];
      }),
    ) as Record<IncomingMessageDto['type'], string>;
  }

  async publish(message: IncomingMessageDto): Promise<void> {
    const topic = this.topics[message.type];
    if (!topic) {
      throw new Error(`Incoming ${message.type} queue topic not configured`);
    }

    try {
      if (Date.now() >= replyDeadline(message.timestamp)) return;
      if (
        message.type === 'text' &&
        this.configService.getOrThrow<boolean>('agent.enabled')
      ) {
        if (!(await this.inbox.enqueue(message))) {
          await this.outgoing.publish({
            traceId: message.traceId,
            correlationId: message.messageId,
            messageId: `${message.to}:${message.messageId}:inbox-full`,
            phoneNumberId: message.to,
            to: message.from,
            type: 'text',
            expiresAt: replyDeadline(message.timestamp),
            content: {
              text: 'There are too many messages waiting. Please wait for my reply, then resend this message.',
            },
          });
          return;
        }
      }
      await this.queueService.publish(
        topic,
        message,
        `${message.to}:${message.messageId}`,
      );
      this.logger.log(
        `Incoming ${message.type} message published to queue. MessageID: ${message.messageId}, From: ${maskPhoneNumber(message.from)}`,
      );
    } catch (error) {
      this.logger.error(
        `Failed to publish incoming ${message.type} message to queue: ${error instanceof Error ? error.message : 'Unknown error'}`,
        error instanceof Error ? error.stack : '',
      );
      throw error;
    }
  }
}
