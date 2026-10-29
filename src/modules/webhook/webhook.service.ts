import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as crypto from 'crypto';
import { randomUUID } from 'node:crypto';
import { event } from '../../common/utils/telemetry';
import { replyDeadline } from '../../lib/validation/message-age';
import { maskPhoneNumber } from '../../common/utils/logger.utils';
import type {
  WebhookPayload,
  WebhookChange,
  WhatsAppMessage,
  WhatsAppMetadata,
  MessageContext,
  Contact,
} from './dto/webhook-event.dto';
import { OutgoingProducer } from '../queue/outgoing-producer';
import { TypingIndicatorService } from '../whatsapp/typing-indicator.service';
import { IncomingProducer } from '../queue/incoming-producer';
import {
  IncomingMessageContext,
  IncomingMessageDto,
  IncomingMessageMetadata,
  IncomingTextSource,
} from '../queue/contracts/message.dto';

type MediaMessageType = 'image' | 'video' | 'audio' | 'document';

/** Common envelope shared by every standardized incoming format. */
type IncomingEnvelope = {
  traceId: string;
  messageId: string;
  from: string;
  to: string;
  timestamp: string;
  contactName: string | null;
  context: IncomingMessageContext | null;
  metadata: IncomingMessageMetadata;
};

type MediaDetails = {
  mediaId: string;
  mimeType: string | null;
  caption: string | null;
  filename: string | null;
  sha256: string | null;
  isVoice: boolean;
};

@Injectable()
export class WebhookService {
  private readonly logger = new Logger(WebhookService.name);

  constructor(
    private readonly outgoingProducer: OutgoingProducer,
    private readonly typingIndicator: TypingIndicatorService,
    private readonly configService: ConfigService,
    private readonly incomingProducer: IncomingProducer,
  ) {}

  private buildContext(
    context: MessageContext | undefined,
  ): IncomingMessageContext | null {
    if (!context) {
      return null;
    }

    return {
      messageId: context.id,
      from: context.from,
    };
  }

  private resolveContactName(
    contacts: Contact[] | undefined,
    waId: string,
  ): string | null {
    if (!contacts || contacts.length === 0) {
      return null;
    }

    const match = contacts.find((contact) => contact.wa_id === waId);
    return (match ?? contacts[0]).profile?.name ?? null;
  }

  private buildEnvelope(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
    contactName: string | null,
    traceId: string,
  ): IncomingEnvelope {
    return {
      traceId,
      messageId: message.id,
      from: message.from,
      to: metadata.phone_number_id,
      timestamp: message.timestamp,
      contactName,
      context: this.buildContext(message.context),
      metadata: {
        phoneNumberId: metadata.phone_number_id,
        displayPhoneNumber: metadata.display_phone_number,
      },
    };
  }

  /**
   * Collapse the several text-bearing message shapes (plain text, interactive
   * button/list reply, template quick reply) into a single normalized payload.
   */
  private normalizeTextPayload(message: WhatsAppMessage): {
    text: string;
    source: IncomingTextSource;
    replyId: string | null;
  } | null {
    switch (message.type) {
      case 'text': {
        const body = message.text?.body;
        return body ? { text: body, source: 'text', replyId: null } : null;
      }
      case 'interactive': {
        const buttonReply = message.interactive?.button_reply;
        if (buttonReply) {
          return {
            text: buttonReply.title,
            source: 'button_reply',
            replyId: buttonReply.id,
          };
        }

        const listReply = message.interactive?.list_reply;
        if (listReply) {
          return {
            text: listReply.title,
            source: 'list_reply',
            replyId: listReply.id,
          };
        }

        return null;
      }
      case 'button': {
        const button = message.button;
        return button?.text
          ? {
              text: button.text,
              source: 'quick_reply',
              replyId: button.payload ?? null,
            }
          : null;
      }
      default:
        return null;
    }
  }

  private extractMediaDetails(
    message: WhatsAppMessage,
    expectedType: MediaMessageType,
  ): MediaDetails | null {
    const media =
      expectedType === 'image'
        ? message.image
        : expectedType === 'video'
          ? message.video
          : expectedType === 'audio'
            ? message.audio
            : message.document;

    if (!media?.id) {
      return null;
    }

    return {
      mediaId: media.id,
      mimeType: media.mime_type ?? null,
      caption: expectedType === 'audio' ? null : (media.caption ?? null),
      filename:
        expectedType === 'document'
          ? (message.document?.filename ?? null)
          : null,
      sha256: media.sha256 ?? null,
      isVoice:
        expectedType === 'audio' ? (message.audio?.voice ?? false) : false,
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

  /**
   * Verify the X-Hub-Signature-256 header to ensure the request is from Meta.
   */
  verifySignature(rawBody: string | Buffer, signature: string): boolean {
    if (!/^sha256=[a-fA-F0-9]{64}$/.test(signature)) {
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

      const signatureBuffer = Buffer.from(signatureHash, 'hex');
      const expectedBuffer = Buffer.from(expectedSignature, 'hex');

      if (signatureBuffer.length !== expectedBuffer.length) {
        this.logger.warn('Signature verification failed (length mismatch)');
        return false;
      }

      // Timing-safe comparison prevents timing attacks.
      const isValid = crypto.timingSafeEqual(signatureBuffer, expectedBuffer);

      if (!isValid) {
        this.logger.warn('Signature verification failed');
      }

      return isValid;
    } catch (error) {
      this.logger.error('Error verifying signature:', error);
      return false;
    }
  }

  async processWebhookEvent(
    payload: Record<string, unknown>,
    traceId: string = randomUUID(),
  ): Promise<void> {
    if (!this.isWebhookPayload(payload)) {
      this.logger.error('Invalid webhook payload structure');
      return;
    }

    this.logger.log(`Processing webhook for object: ${payload.object}`);

    for (const entry of payload.entry) {
      for (const change of entry.changes ?? []) {
        await this.handleWhatsAppChange(change, traceId);
      }
    }
  }

  private async handleWhatsAppChange(
    change: WebhookChange,
    traceId: string,
  ): Promise<void> {
    if (change.field !== 'messages') {
      return;
    }

    this.logStatusUpdates(change, traceId);

    if (!change.value.messages) {
      return;
    }

    for (const message of change.value.messages) {
      const contactName = this.resolveContactName(
        change.value.contacts,
        message.from,
      );
      await this.handleIncomingWhatsAppMessage(
        message,
        change.value.metadata,
        contactName,
        traceId,
      );
    }
  }

  /**
   * Log delivery-status updates for messages we sent. A message the API
   * accepted can still fail afterwards (e.g. an unreachable media link or a
   * closed customer service window); the reason only surfaces here.
   */
  private logStatusUpdates(change: WebhookChange, traceId: string): void {
    for (const status of change.value.statuses ?? []) {
      event(this.logger, 'delivery.status', {
        trace_id: traceId,
        provider_message_id: status.id,
        delivery_status: status.status,
        error_codes: status.errors?.slice(0, 5).map((error) => error.code),
      });
    }
  }

  /**
   * Route an incoming message to the handler for its standardized format.
   */
  private async handleIncomingWhatsAppMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
    contactName: string | null,
    traceId: string,
  ): Promise<void> {
    event(this.logger, 'webhook.message.received', {
      trace_id: traceId,
      correlation_id: message.id,
      message_type: message.type,
    });
    let deadline: number;
    try {
      deadline = replyDeadline(message.timestamp);
    } catch {
      this.logger.warn('Ignoring invalid message timestamp');
      return;
    }
    if (Date.now() >= deadline) {
      this.logger.warn('Ignoring incoming message older than 20 hours');
      return;
    }
    if (
      metadata.phone_number_id !==
      this.configService.getOrThrow<string>('whatsapp.phoneNumberId')
    ) {
      this.logger.warn('Ignoring webhook message for another business number');
      return;
    }

    switch (message.type) {
      case 'text':
      case 'interactive':
      case 'button':
        await this.handleTextMessage(message, metadata, contactName, traceId);
        break;
      case 'image':
      case 'video':
      case 'audio':
      case 'document':
        if (this.configService.get<boolean>('whatsapp.mediaSupported')) {
          await this.handleMediaMessage(
            message,
            metadata,
            contactName,
            message.type,
            traceId,
          );
        } else {
          this.logger.log(
            `Media support is disabled. Replying fallback for: ${message.type}`,
          );
          const reply = this.getMediaFallbackReply(message.type);
          await this.publishFallback(message, metadata, reply, traceId);
        }
        break;
      default: {
        this.logger.log(`Unsupported message type: ${message.type}`);
        const reply = this.getMediaFallbackReply(message.type);
        await this.publishFallback(message, metadata, reply, traceId);
        break;
      }
    }
  }

  private async publishFallback(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
    reply: string,
    traceId: string,
  ): Promise<void> {
    await this.outgoingProducer.publish({
      traceId,
      correlationId: message.id,
      expiresAt: replyDeadline(message.timestamp),
      messageId: `${metadata.phone_number_id}:${message.id}:fallback`,
      phoneNumberId: metadata.phone_number_id,
      to: message.from,
      type: 'text',
      content: { text: reply },
    });
  }

  private startTyping(message: WhatsAppMessage): void {
    void this.typingIndicator
      .start(message.id, message.from)
      .catch((error: unknown) => {
        this.logger.warn(
          `Typing indicator unavailable: ${error instanceof Error ? error.message : 'Unknown error'}`,
        );
      });
  }

  private getMediaFallbackReply(type: string): string {
    return `Eitri currently supports text messages. Please describe your ${type} in text.`;
  }

  private async handleTextMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
    contactName: string | null,
    traceId: string,
  ): Promise<void> {
    const payload = this.normalizeTextPayload(message);
    if (!payload) {
      this.logger.warn(`Text message ${message.id} has no readable content`);
      return;
    }

    await this.incomingProducer.publish({
      ...this.buildEnvelope(message, metadata, contactName, traceId),
      type: 'text',
      text: payload.text,
      source: payload.source,
      replyId: payload.replyId,
    });
    this.startTyping(message);
  }

  /**
   * Publish a standardized media message. The bytes are not fetched here; the
   * WhatsApp `mediaId` is forwarded so a downstream consumer can download and
   * store the file.
   */
  private async handleMediaMessage(
    message: WhatsAppMessage,
    metadata: WhatsAppMetadata,
    contactName: string | null,
    type: MediaMessageType,
    traceId: string,
  ): Promise<void> {
    this.logger.log(
      `Received ${type} message from ${maskPhoneNumber(message.from)}: ${message.id}`,
    );

    const details = this.extractMediaDetails(message, type);
    if (!details) {
      this.logger.error(`No media ID found for ${type} message`);
      return;
    }

    const base = {
      ...this.buildEnvelope(message, metadata, contactName, traceId),
      mediaId: details.mediaId,
      mimeType: details.mimeType,
      sha256: details.sha256,
    };

    const dto: IncomingMessageDto =
      type === 'audio'
        ? { ...base, type, voice: details.isVoice }
        : type === 'document'
          ? {
              ...base,
              type,
              caption: details.caption,
              filename: details.filename,
            }
          : { ...base, type, caption: details.caption };

    await this.incomingProducer.publish(dto);
    this.startTyping(message);
  }
}
