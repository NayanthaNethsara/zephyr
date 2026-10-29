import { Injectable } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { OutgoingMessageDto } from './contracts/message.dto';
import { outgoingMessageSchema } from './contracts/outgoing.schema';
import { QueueService } from './queue.service';

@Injectable()
export class OutgoingProducer {
  constructor(
    private readonly queue: QueueService,
    private readonly config: ConfigService,
  ) {}

  async publish(message: OutgoingMessageDto): Promise<void> {
    const payload = outgoingMessageSchema.parse(message);
    if (payload.replyToMessageId && !payload.expiresAt)
      throw new Error('Replies require an expiry deadline');
    if (payload.expiresAt && Date.now() >= payload.expiresAt) return;
    await this.queue.publish(
      this.config.getOrThrow<string>('queue.topics.outgoing'),
      payload,
      message.messageId,
    );
  }
}
