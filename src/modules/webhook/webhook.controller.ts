import {
  Controller,
  Get,
  Post,
  Query,
  Body,
  Headers,
  HttpCode,
  HttpStatus,
  UnauthorizedException,
  BadRequestException,
  Logger,
  Req,
  ServiceUnavailableException,
} from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { WebhookService } from './webhook.service';
import { setPublicBaseUrl } from '../whatsapp/interactive.mapper';

interface FastifyRequestWithRawBody {
  id: string;
  rawBody?: Buffer;
  headers: Record<string, string | string[] | undefined>;
}

@Controller('whatsapp/webhooks')
export class WebhookController {
  private readonly logger = new Logger(WebhookController.name);

  constructor(
    private readonly webhookService: WebhookService,
    private readonly configService: ConfigService,
  ) {}

  private syncPublicUrl(
    headers: Record<string, string | string[] | undefined>,
  ): void {
    const host = (headers['x-forwarded-host'] || headers['host']) as
      string | undefined;
    const proto = ((headers['x-forwarded-proto'] as string) || 'https')
      .split(',')[0]
      .trim();
    if (host && !host.includes('localhost') && !host.includes('127.0.0.1')) {
      setPublicBaseUrl(`${proto}://${host}`);
    }
  }

  /**
   * GET /whatsapp/webhooks - Verification endpoint
   * Meta calls this once during webhook setup
   */
  @Get()
  verifyWebhook(
    @Req() req: FastifyRequestWithRawBody,
    @Query('hub.mode') mode: string,
    @Query('hub.verify_token') token: string,
    @Query('hub.challenge') challenge: string,
  ): string {
    this.syncPublicUrl(req.headers);
    this.logger.log('Webhook verification request received');

    if (!mode || !token) {
      this.logger.error('Missing verification parameters');
      throw new BadRequestException('Missing verification parameters');
    }

    const verifyToken = this.configService.get<string>('meta.verifyToken');
    if (mode === 'subscribe' && token === verifyToken) {
      this.logger.log('Webhook verified successfully');
      return challenge;
    }

    this.logger.error('Invalid verification token');
    throw new UnauthorizedException('Invalid verification token');
  }

  /**
   * POST /whatsapp/webhooks - Receives messages & status updates from Meta
   */
  @Post()
  @HttpCode(HttpStatus.OK)
  async handleWebhook(
    @Req() req: FastifyRequestWithRawBody,
    @Body() body: Record<string, unknown>,
    @Headers('x-hub-signature-256') signature: string | undefined,
  ) {
    this.syncPublicUrl(req.headers);
    this.logger.log('Webhook event received');

    if (req.rawBody === undefined)
      throw new BadRequestException('Missing raw request body');
    const rawBody = req.rawBody;
    if (!this.webhookService.verifySignature(rawBody, signature || '')) {
      this.logger.error('Invalid signature');
      throw new UnauthorizedException('Invalid signature');
    }

    try {
      await this.webhookService.processWebhookEvent(body, req.id);
      return { status: 'ok' };
    } catch (error) {
      this.logger.error('Error processing webhook:', error);
      throw new ServiceUnavailableException(
        'Webhook processing failed; retry delivery.',
      );
    }
  }
}
