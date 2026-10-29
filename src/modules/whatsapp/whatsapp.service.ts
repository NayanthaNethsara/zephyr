import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { PermanentDeliveryError } from '../../common/errors/permanent-delivery.error';
import {
  Template,
  Interactive,
  LocationObject,
  ContactObject,
  WhatsAppApiResponse,
} from '../../types/whatsapp.types';

@Injectable()
export class WhatsAppService {
  private readonly logger = new Logger(WhatsAppService.name);
  private readonly phoneNumberId: string;
  private readonly accessToken: string;
  private readonly baseUrl: string;
  private readonly requestTimeoutMs: number;

  constructor(private readonly configService: ConfigService) {
    this.baseUrl = `https://graph.facebook.com/${configService.getOrThrow<string>('whatsapp.apiVersion')}`;
    this.requestTimeoutMs = configService.getOrThrow<number>(
      'whatsapp.requestTimeoutMs',
    );
    this.phoneNumberId =
      this.configService.get<string>('whatsapp.phoneNumberId') || '';
    this.accessToken =
      this.configService.get<string>('whatsapp.accessToken') || '';
  }

  private async sendRequest(
    endpoint: string,
    data: Record<string, unknown>,
  ): Promise<WhatsAppApiResponse | null> {
    const url = `${this.baseUrl}/${this.phoneNumberId}/${endpoint}`;
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${this.accessToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(data),
      signal: AbortSignal.timeout(this.requestTimeoutMs),
    });
    if (!response.ok) {
      await response.body?.cancel();
      if (
        response.status >= 400 &&
        response.status < 500 &&
        response.status !== 429 &&
        response.status !== 408
      ) {
        throw new PermanentDeliveryError(
          `WhatsApp rejected delivery (HTTP ${response.status})`,
        );
      }
      throw new Error(
        `WhatsApp API temporarily unavailable (HTTP ${response.status})`,
      );
    }
    const result: unknown = await response.json();
    if (typeof result !== 'object' || result === null) {
      throw new Error('Invalid WhatsApp API response');
    }
    return result as WhatsAppApiResponse;
  }

  /**
   * Build the shared envelope for a /messages request. `replyToMessageId`
   * quotes an earlier message via the WhatsApp `context` field.
   */
  private messageEnvelope(
    to: string,
    replyToMessageId?: string,
  ): Record<string, unknown> {
    return {
      messaging_product: 'whatsapp',
      recipient_type: 'individual',
      to,
      ...(replyToMessageId && { context: { message_id: replyToMessageId } }),
    };
  }

  /**
   * Send a text message
   */
  async sendTextMessage(
    to: string,
    text: string,
    previewUrl?: boolean,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'text',
      text: { body: text, ...(previewUrl && { preview_url: true }) },
    });
  }

  /**
   * Send media message (image, video, audio, sticker)
   */
  async sendMediaMessage(
    to: string,
    mediaType: 'image' | 'video' | 'audio' | 'sticker',
    mediaIdOrUrl: string,
    caption?: string,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    const isUrl = mediaIdOrUrl.startsWith('http');
    const mediaPayload: Record<string, string> = isUrl
      ? { link: mediaIdOrUrl }
      : { id: mediaIdOrUrl };

    if (caption && (mediaType === 'image' || mediaType === 'video')) {
      mediaPayload.caption = caption;
    }

    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: mediaType,
      [mediaType]: mediaPayload,
    });
  }

  /**
   * Send document message
   */
  async sendDocumentMessage(
    to: string,
    documentIdOrUrl: string,
    caption?: string,
    filename?: string,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    const isUrl = documentIdOrUrl.startsWith('http');
    const documentPayload: Record<string, string> = isUrl
      ? { link: documentIdOrUrl }
      : { id: documentIdOrUrl };

    if (caption) {
      documentPayload.caption = caption;
    }

    if (filename) {
      documentPayload.filename = filename;
    }

    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'document',
      document: documentPayload,
    });
  }

  /**
   * Send location message
   */
  async sendLocationMessage(
    to: string,
    location: LocationObject,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'location',
      location,
    });
  }

  /**
   * Send contact cards
   */
  async sendContactsMessage(
    to: string,
    contacts: ContactObject[],
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'contacts',
      contacts,
    });
  }

  /**
   * React to a message with an emoji; an empty emoji removes the reaction.
   */
  async sendReactionMessage(
    to: string,
    messageId: string,
    emoji: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to),
      type: 'reaction',
      reaction: { message_id: messageId, emoji },
    });
  }

  /**
   * Send template message
   */
  async sendTemplateMessage(
    to: string,
    template: Template,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'template',
      template: {
        name: template.name,
        language: {
          code: template.language,
        },
        components: template.components ?? [],
      },
    });
  }

  /**
   * Send interactive message (buttons, list, cta_url, location request,
   * media carousel)
   */
  async sendInteractiveMessage(
    to: string,
    interactive: Interactive,
    replyToMessageId?: string,
  ): Promise<WhatsAppApiResponse | null> {
    return this.sendRequest('messages', {
      ...this.messageEnvelope(to, replyToMessageId),
      type: 'interactive',
      interactive,
    });
  }

  /**
   * Mark message as read and optionally show typing indicator
   */
  async markMessageAsRead(
    messageId: string,
    showTyping = false,
  ): Promise<WhatsAppApiResponse | null> {
    const payload: Record<string, unknown> = {
      messaging_product: 'whatsapp',
      status: 'read',
      message_id: messageId,
    };

    // Add typing indicator if requested
    if (showTyping) {
      payload.typing_indicator = {
        type: 'text',
      };
    }

    return this.sendRequest('messages', payload);
  }
}
