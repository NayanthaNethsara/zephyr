import { Injectable, OnModuleInit, Logger } from '@nestjs/common';
import { event, queueTelemetry } from '../../common/utils/telemetry';
import { ConfigService } from '@nestjs/config';
import { QueueService } from './queue.service';
import { InvalidQueueMessage } from './queue.types';
import { outgoingMessageSchema } from './contracts/outgoing.schema';
import { WhatsAppService } from '../whatsapp/whatsapp.service';
import { TypingIndicatorService } from '../whatsapp/typing-indicator.service';
import {
  toWireInteractive,
  toWireContact,
} from '../whatsapp/interactive.mapper';
import { WhatsAppApiResponse } from '../../types/whatsapp.types';
import {
  OutgoingMessageDto,
  OutgoingMediaContent,
} from './contracts/message.dto';

@Injectable()
export class OutgoingMessageConsumer implements OnModuleInit {
  private readonly logger = new Logger(OutgoingMessageConsumer.name);
  private readonly outgoingTopic: string;

  constructor(
    private readonly configService: ConfigService,
    private readonly queueService: QueueService,
    private readonly whatsappService: WhatsAppService,
    private readonly typingIndicator: TypingIndicatorService,
  ) {
    this.outgoingTopic =
      this.configService.get<string>('queue.topics.outgoing') || '';
  }

  async onModuleInit() {
    await this.subscribeToTopic(this.outgoingTopic);
  }

  private mediaReference(content: OutgoingMediaContent): string | null {
    return content.mediaUrl ?? content.mediaId ?? null;
  }

  /**
   * Route an outgoing message to the matching WhatsApp send call. Returns
   * the API response, or null when validation or the send fails.
   */
  private async dispatchSend(
    message: OutgoingMessageDto,
  ): Promise<WhatsAppApiResponse | null> {
    switch (message.type) {
      case 'text': {
        if (!message.content.text) {
          this.logger.error('Invalid text message - missing text');
          return null;
        }
        return this.whatsappService.sendTextMessage(
          message.to,
          message.content.text,
          message.content.previewUrl,
          message.replyToMessageId,
        );
      }

      case 'image':
      case 'video':
      case 'audio':
      case 'sticker': {
        const reference = this.mediaReference(message.content);
        if (!reference) {
          this.logger.error(
            `Invalid ${message.type} message - missing media reference`,
          );
          return null;
        }
        const caption =
          message.type === 'image' || message.type === 'video'
            ? message.content.caption
            : undefined;
        return this.whatsappService.sendMediaMessage(
          message.to,
          message.type,
          reference,
          caption,
          message.replyToMessageId,
        );
      }

      case 'document': {
        const reference = this.mediaReference(message.content);
        if (!reference) {
          this.logger.error(
            'Invalid document message - missing media reference',
          );
          return null;
        }
        return this.whatsappService.sendDocumentMessage(
          message.to,
          reference,
          message.content.caption,
          message.content.filename,
          message.replyToMessageId,
        );
      }

      case 'location': {
        const { latitude, longitude } = message.content;
        if (typeof latitude !== 'number' || typeof longitude !== 'number') {
          this.logger.error('Invalid location message - missing coordinates');
          return null;
        }
        return this.whatsappService.sendLocationMessage(
          message.to,
          message.content,
          message.replyToMessageId,
        );
      }

      case 'contacts': {
        if (!message.content.contacts?.length) {
          this.logger.error('Invalid contacts message - missing contacts');
          return null;
        }
        return this.whatsappService.sendContactsMessage(
          message.to,
          message.content.contacts.map(toWireContact),
          message.replyToMessageId,
        );
      }

      case 'reaction': {
        if (!message.content.messageId) {
          this.logger.error('Invalid reaction message - missing messageId');
          return null;
        }
        return this.whatsappService.sendReactionMessage(
          message.to,
          message.content.messageId,
          message.content.emoji ?? '',
        );
      }

      case 'interactive': {
        const interactive = message.content.interactive;
        if (!interactive?.type || !interactive.body?.text) {
          this.logger.error(
            'Invalid interactive message - missing type or body text',
          );
          return null;
        }
        const publicBaseUrl = this.configService.get<string>(
          'whatsapp.publicBaseUrl',
        );
        return this.whatsappService.sendInteractiveMessage(
          message.to,
          toWireInteractive(interactive, publicBaseUrl),
          message.replyToMessageId,
        );
      }

      case 'template': {
        if (!message.content.template?.name) {
          this.logger.error('Invalid template message - missing template name');
          return null;
        }
        return this.whatsappService.sendTemplateMessage(
          message.to,
          message.content.template,
          message.replyToMessageId,
        );
      }
    }
  }

  private async sendWhatsAppMessage(
    message: OutgoingMessageDto,
  ): Promise<void> {
    const response = await this.dispatchSend(message);
    if (response === null) throw new Error('WhatsApp delivery failed');
    const messageId = response.messages?.[0]?.id;
    if (!messageId) throw new Error('WhatsApp returned no message ID');
    event(this.logger, 'delivery.sent', {
      ...queueTelemetry(message),
      provider_message_id: messageId,
      message_type: message.type,
    });
  }

  private async subscribeToTopic(subject: string): Promise<void> {
    this.logger.log(`Starting to consume queue topic: ${subject}`);
    await this.queueService.subscribe(subject, async (payload) => {
      const parsed = outgoingMessageSchema.safeParse(payload);
      if (!parsed.success) {
        throw new InvalidQueueMessage('Invalid outgoing message contract');
      }
      const message: OutgoingMessageDto = parsed.data;
      if (
        message.phoneNumberId &&
        message.phoneNumberId !==
          this.configService.getOrThrow<string>('whatsapp.phoneNumberId')
      )
        throw new InvalidQueueMessage(
          'Outgoing message belongs to another business number',
        );
      this.typingIndicator.stop(message.to);
      // Old queue entries without a deadline cannot safely be sent as replies.
      if (
        (message.replyToMessageId && !message.expiresAt) ||
        (message.expiresAt &&
          Date.now() +
            this.configService.getOrThrow<number>(
              'whatsapp.requestTimeoutMs',
            ) >=
            message.expiresAt)
      ) {
        this.logger.warn('Expired or undated reply discarded without sending');
        event(this.logger, 'delivery.expired', queueTelemetry(message));
        return;
      }
      try {
        await this.sendWhatsAppMessage(message);
      } catch (error) {
        event(this.logger, 'delivery.failed', {
          ...queueTelemetry(message),
          error_type: error instanceof Error ? error.name : 'UnknownError',
        });
        throw error;
      }
    });
  }
}
