import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as crypto from 'crypto';
import type {
  MessageContext,
  WebhookChange,
  WebhookPayload,
  WhatsAppMessage,
  WhatsAppMetadata,
  WhatsAppValue,
} from './dto/webhook-event.dto';
import { IncomingMessageProducer } from '../nats/incoming-message-producer';
import { IncomingWhatsAppMessageDto } from '../nats/dto/nats-message.dto';
import { WhatsAppService } from '../whatsapp/whatsapp.service';

@Injectable()
export class WebhookService {
  private readonly logger = new Logger(WebhookService.name);

  constructor(
    private readonly whatsappService: WhatsAppService,
    private readonly configService: ConfigService,
    private readonly incomingMessageProducer: IncomingMessageProducer,
  ) {}

  private buildContext(
    context: MessageContext | undefined,
  ): IncomingWhatsAppMessageDto['context'] {
    if (!context) {
      return null;
    }

    return {
      messageId: context.id,
      from: context.from,
    };
  }

  private isWebhookPayload(payload: unknown): payload is WebhookPayload {
    return (
      typeof payload === 'object' &&
      payload !== null &&
      'object' in payload &&
      typeof (payload as Record<string, unknown>).object === 'string' &&
      'entry' in payload &&
      Array.isArray((payload as Record<string, unknown>).entry)
    );
  }

  verifySignature(rawBody: string, signature: string): boolean {
    if (!signature) {
      this.logger.warn('No signature provided in request');
      return false;
    }

    const appSecret = this.configService.get<string>('meta.appSecret');
    if (!appSecret) {
      this.logger.error('META_APP_SECRET is not configured');
      return false;
    }

    try {
      const expectedSignature = crypto
        .createHmac('sha256', appSecret)
        .update(rawBody)
        .digest('hex');

      const signatureHash = signature.startsWith('sha256=')
        ? signature.substring(7)
        : signature;

      return crypto.timingSafeEqual(
        Buffer.from(signatureHash, 'hex'),
        Buffer.from(expectedSignature, 'hex'),
      );
    } catch (error) {
      this.logger.error('Error verifying signature:', error);
      return false;
    }
  }

  async processWebhookEvent(payload: Record<string, unknown>): Promise<void> {
    if (!this.isWebhookPayload(payload)) {
      this.logger.error('Invalid webhook payload structure');
      return;
    }

    if (!payload.entry || payload.entry.length === 0) {
      this.logger.warn('No entries in webhook payload');
      return;
    }

    for (const entry of payload.entry) {
      if (entry.changes) {
        for (const change of entry.changes) {
          await this.handleWhatsAppChange(change);
        }
      }
    }
  }

  private async handleWhatsAppChange(change: WebhookChange): Promise<void> {
    if (change.field !== 'messages') {
      return;
    }

    const value: WhatsAppValue = change.value;
    if (!value.messages) {
      return;
    }

    for (const message of value.messages) {
      await this.handleIncomingWhatsAppMessage(message, value.metadata);
    }
  }

  private async handleIncomingWhatsAppMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
  ): Promise<void> {
    switch (message.type) {
      case 'text':
        await this.handleTextMessage(message, metadata);
        break;
      case 'interactive':
      case 'button':
        await this.handleInteractiveMessage(message, metadata);
        break;
      default:
        this.logger.log(`Ignoring non-text message type for now: ${message.type}`);
    }
  }

  private async handleTextMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
  ): Promise<void> {
    const { id: messageId, from } = message;
    if (!message.text?.body) {
      this.logger.warn(`Text message ${messageId} has no body content`);
      return;
    }

    await this.whatsappService.markMessageAsRead(messageId, true);

    const incomingMessageDto: IncomingWhatsAppMessageDto = {
      messageId,
      from,
      to: metadata.phone_number_id,
      timestamp: message.timestamp,
      type: 'text',
      content: message.text.body,
      context: this.buildContext(message.context),
      metadata: {
        phoneNumberId: metadata.phone_number_id,
        displayPhoneNumber: metadata.display_phone_number,
      },
    };

    await this.incomingMessageProducer.sendMessage(incomingMessageDto);
  }

  private async handleInteractiveMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
  ): Promise<void> {
    const { id: messageId, from } = message;
    if (!message.interactive) {
      this.logger.warn(
        `Interactive message ${messageId} has no interactive content`,
      );
      return;
    }

    const selectedText =
      message.interactive.button_reply?.title ||
      message.interactive.list_reply?.title;

    if (!selectedText) {
      this.logger.warn(`Interactive message ${messageId} has no selected text`);
      return;
    }

    await this.whatsappService.markMessageAsRead(messageId, true);

    const incomingMessageDto: IncomingWhatsAppMessageDto = {
      messageId,
      from,
      to: metadata.phone_number_id,
      timestamp: message.timestamp,
      type: 'text',
      content: selectedText,
      context: this.buildContext(message.context),
      metadata: {
        phoneNumberId: metadata.phone_number_id,
        displayPhoneNumber: metadata.display_phone_number,
      },
    };

    await this.incomingMessageProducer.sendMessage(incomingMessageDto);
  }
}
