export interface IncomingMessageMetadata {
  phoneNumberId: string;
  displayPhoneNumber: string;
}

export interface IncomingMessageContext {
  messageId: string;
  from: string;
}

interface IncomingMessageEnvelope {
  traceId?: string;
  messageId: string;
  from: string;
  to: string;
  timestamp: string;
  contactName: string | null;
  context: IncomingMessageContext | null;
  metadata: IncomingMessageMetadata;
}

export type IncomingTextSource =
  'text' | 'button_reply' | 'list_reply' | 'quick_reply';

export interface IncomingTextMessageDto extends IncomingMessageEnvelope {
  type: 'text';
  text: string;
  source: IncomingTextSource;
  replyId: string | null;
}

interface IncomingMediaEnvelope extends IncomingMessageEnvelope {
  mediaId: string;
  mimeType: string | null;
  sha256: string | null;
}

export interface IncomingImageMessageDto extends IncomingMediaEnvelope {
  type: 'image';
  caption: string | null;
}

export interface IncomingVideoMessageDto extends IncomingMediaEnvelope {
  type: 'video';
  caption: string | null;
}

export interface IncomingAudioMessageDto extends IncomingMediaEnvelope {
  type: 'audio';
  voice: boolean;
}

export interface IncomingDocumentMessageDto extends IncomingMediaEnvelope {
  type: 'document';
  caption: string | null;
  filename: string | null;
}

export type IncomingMessageDto =
  | IncomingTextMessageDto
  | IncomingImageMessageDto
  | IncomingVideoMessageDto
  | IncomingAudioMessageDto
  | IncomingDocumentMessageDto;

interface OutgoingMessageBase {
  traceId?: string;
  correlationId?: string;
  /** Original incoming message timestamp + 20 hours, in milliseconds. */
  expiresAt?: number;
  phoneNumberId?: string;
  messageId?: string;
  to: string;
  replyToMessageId?: string;
}

export interface OutgoingTextMessageDto extends OutgoingMessageBase {
  type: 'text';
  content: { text: string; previewUrl?: boolean };
}

export interface OutgoingMediaContent {
  mediaUrl?: string;
  mediaId?: string;
  caption?: string;
}

export interface OutgoingImageMessageDto extends OutgoingMessageBase {
  type: 'image';
  content: OutgoingMediaContent;
}

export interface OutgoingVideoMessageDto extends OutgoingMessageBase {
  type: 'video';
  content: OutgoingMediaContent;
}

export interface OutgoingAudioMessageDto extends OutgoingMessageBase {
  type: 'audio';
  content: Omit<OutgoingMediaContent, 'caption'>;
}

export interface OutgoingDocumentMessageDto extends OutgoingMessageBase {
  type: 'document';
  content: OutgoingMediaContent & { filename?: string };
}

export interface OutgoingStickerMessageDto extends OutgoingMessageBase {
  type: 'sticker';
  content: Omit<OutgoingMediaContent, 'caption'>;
}

export interface OutgoingLocationMessageDto extends OutgoingMessageBase {
  type: 'location';
  content: {
    latitude: number;
    longitude: number;
    name?: string;
    address?: string;
  };
}

export interface OutgoingContactCard {
  name: {
    formattedName: string;
    firstName?: string;
    lastName?: string;
  };
  phones?: Array<{ phone: string; type?: string; waId?: string }>;
  emails?: Array<{ email: string; type?: string }>;
  urls?: Array<{ url: string; type?: string }>;
  org?: { company?: string; department?: string; title?: string };
}

export interface OutgoingContactsMessageDto extends OutgoingMessageBase {
  type: 'contacts';
  content: { contacts: OutgoingContactCard[] };
}

export interface OutgoingReactionMessageDto extends OutgoingMessageBase {
  type: 'reaction';
  content: { messageId: string; emoji: string };
}

export interface OutgoingInteractiveHeader {
  type: 'text' | 'image' | 'video' | 'document';
  text?: string | null;
  mediaUrl?: string | null;
  mediaId?: string | null;
}

export interface OutgoingReplyButton {
  type: 'reply';
  reply: { id: string; title: string };
}

export interface OutgoingCarouselCard {
  header: {
    type: 'image' | 'video';
    mediaUrl?: string | null;
    mediaId?: string | null;
  };
  body?: { text: string } | null;
  footer?: { text: string } | null;
  ctaUrl?: { displayText: string; url: string } | null;
  buttons?: OutgoingReplyButton[] | null;
}

export type OutgoingInteractiveContent =
  | {
      type: 'button';
      header?: OutgoingInteractiveHeader | null;
      body: { text: string };
      footer?: { text: string } | null;
      action: { buttons: OutgoingReplyButton[] };
    }
  | {
      type: 'list';
      header?: OutgoingInteractiveHeader | null;
      body: { text: string };
      footer?: { text: string } | null;
      action: {
        button: string;
        sections: Array<{
          title?: string | null;
          rows: Array<{
            id: string;
            title: string;
            description?: string | null;
          }>;
        }>;
      };
    }
  | {
      type: 'cta_url';
      header?: OutgoingInteractiveHeader | null;
      body: { text: string };
      footer?: { text: string } | null;
      action: { displayText: string; url: string };
    }
  | {
      type: 'location_request_message';
      body: { text: string };
    }
  | {
      type: 'carousel';
      body: { text: string };
      cards: OutgoingCarouselCard[];
    }
  | {
      type: 'address_message';
      body: { text: string };
      footer?: { text: string } | null;
      action: {
        country: string;
        values?: Record<string, unknown> | null;
        savedAddresses?: Array<{
          id: string;
          value: Record<string, unknown>;
        }> | null;
      };
    }
  | {
      type: 'flow';
      header?: OutgoingInteractiveHeader | null;
      body: { text: string };
      footer?: { text: string } | null;
      action: {
        cta: string;
        flowId?: string | null;
        flowName?: string | null;
        flowToken?: string | null;
        flowAction?: 'navigate' | 'data_exchange' | null;
        screen?: string | null;
        data?: Record<string, unknown> | null;
        mode?: 'draft' | 'published' | null;
      };
    }
  | {
      type: 'call_permission_request';
      body: { text: string };
    };

export interface OutgoingInteractiveMessageDto extends OutgoingMessageBase {
  type: 'interactive';
  content: { interactive: OutgoingInteractiveContent };
}

export interface OutgoingTemplateContent {
  name: string;
  language: string;
  components?: Array<{
    type: string;
    parameters: Array<{
      type: string;
      text?: string;
      image?: { link: string };
      video?: { link: string };
      document?: { link: string };
    }>;
  }>;
}

export interface OutgoingTemplateMessageDto extends OutgoingMessageBase {
  type: 'template';
  content: { template: OutgoingTemplateContent };
}

export type OutgoingMessageDto =
  | OutgoingTextMessageDto
  | OutgoingImageMessageDto
  | OutgoingVideoMessageDto
  | OutgoingAudioMessageDto
  | OutgoingDocumentMessageDto
  | OutgoingStickerMessageDto
  | OutgoingLocationMessageDto
  | OutgoingContactsMessageDto
  | OutgoingReactionMessageDto
  | OutgoingInteractiveMessageDto
  | OutgoingTemplateMessageDto;
