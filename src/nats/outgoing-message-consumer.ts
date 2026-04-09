import {
  Injectable,
  OnModuleInit,
  OnModuleDestroy,
  Logger,
} from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { NatsService } from './nats.service';
import { WhatsAppService } from '../whatsapp/whatsapp.service';
import { OutgoingWhatsAppMessageDto } from './dto/nats-message.dto';

type LegacyOutgoingWhatsAppMessage = OutgoingWhatsAppMessageDto & {
  text?: string;
};

@Injectable()
export class OutgoingMessageConsumer implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(OutgoingMessageConsumer.name);
  private readonly textSubject: string;

  constructor(
    private readonly configService: ConfigService,
    private readonly natsService: NatsService,
    private readonly whatsappService: WhatsAppService,
  ) {
    this.textSubject =
      this.configService.get<string>('nats.subjects.outgoingText') || '';

    if (!this.textSubject) {
      this.logger.warn(
        'Outgoing text NATS subject not configured. Set NATS_SUBJECT_OUTGOING_TEXT in environment',
      );
    }
  }

  async onModuleInit() {
    if (!this.textSubject) {
      this.logger.error(
        'Outgoing text NATS subject not configured. Consumer not started.',
      );
      return;
    }

    await this.subscribeToSubject(
      this.textSubject,
      'whatsapp_gateway_outgoing_text',
    );
  }

  onModuleDestroy() {
    // NATS service handles disconnection
  }

  private normalizeOutgoingMessage(
    payload: LegacyOutgoingWhatsAppMessage,
  ): OutgoingWhatsAppMessageDto | null {
    const content = payload.content ?? {};
    const normalizedType = payload.type ?? 'text';
    const normalizedText = content.text ?? payload.text;

    if (!payload.to) {
      return null;
    }

    if (normalizedType === 'template' && content.template) {
      return {
        to: payload.to,
        type: 'template',
        content: {
          template: content.template,
        },
        replyToMessageId: payload.replyToMessageId,
      };
    }

    if (normalizedType === 'interactive' && content.interactive) {
      return {
        to: payload.to,
        type: 'interactive',
        content: {
          text: normalizedText,
          interactive: content.interactive,
        },
        replyToMessageId: payload.replyToMessageId,
      };
    }

    if (!normalizedText) {
      return null;
    }

    return {
      to: payload.to,
      type: 'text',
      content: {
        text: normalizedText,
      },
      replyToMessageId: payload.replyToMessageId,
    };
  }

  /**
   * Send message via WhatsApp (text or interactive)
   */
  private async sendWhatsAppMessage(
    message: OutgoingWhatsAppMessageDto,
  ): Promise<boolean> {
    try {
      // Validate message structure
      if (!message.to) {
        this.logger.error('Invalid message - missing recipient');
        return false;
      }

      // Handle interactive messages
      if (message.type === 'interactive' && message.content?.interactive) {
        if (!message.content.interactive.body?.text) {
          this.logger.error('Invalid interactive message - missing body text');
          return false;
        }

        await this.whatsappService.sendInteractiveMessage(
          message.to,
          message.content.interactive,
        );

        this.logger.log(`Interactive message sent to ${message.to}`);
        return true;
      }

      if (message.type === 'template' && message.content?.template) {
        await this.whatsappService.sendTemplateMessage(
          message.to,
          message.content.template,
        );

        this.logger.log(`Template message sent to ${message.to}`);
        return true;
      }

      // Handle text messages
      if (!message.content?.text) {
        this.logger.error('Invalid message - missing text content');
        return false;
      }

      await this.whatsappService.sendTextMessage(
        message.to,
        message.content.text,
      );

      this.logger.log(`Text message sent to ${message.to}`);
      return true;
    } catch (error) {
      this.logger.error(
        `Failed to send WhatsApp message: ${error instanceof Error ? error.message : 'Unknown error'}`,
        error instanceof Error ? error.stack : '',
      );
      return false;
    }
  }

  private async subscribeToSubject(
    subject: string,
    durableName: string,
  ): Promise<void> {
    if (!subject) {
      return;
    }

    this.logger.log(`Starting to consume NATS subject: ${subject}`);
    await this.natsService.subscribe(subject, durableName, async (payload) => {
      try {
        const messageContent = JSON.stringify(payload);
        this.logger.log('=== INCOMING NATS MESSAGE ===');
        this.logger.log(`Raw message body: ${messageContent}`);
        this.logger.log('==============================');

        const parsedMessage = this.normalizeOutgoingMessage(
          payload as LegacyOutgoingWhatsAppMessage,
        );

        if (!parsedMessage) {
          this.logger.error('Invalid message: unsupported outgoing payload');
          return;
        }

        this.logger.log(
          `Processing outgoing ${parsedMessage.type} message to: ${parsedMessage.to}`,
        );

        await this.sendWhatsAppMessage(parsedMessage);
      } catch (error) {
        this.logger.error(
          `Error processing message: ${error instanceof Error ? error.message : 'Unknown error'}`,
          error instanceof Error ? error.stack : '',
        );
      }
    });
  }
}
