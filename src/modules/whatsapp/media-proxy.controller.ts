import {
  Controller,
  Get,
  Query,
  Res,
  HttpStatus,
  Logger,
} from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import type { FastifyReply } from 'fastify';

@Controller('whatsapp/media')
export class MediaProxyController {
  private readonly logger = new Logger(MediaProxyController.name);

  constructor(private readonly config: ConfigService) {}

  @Get('image-proxy')
  async proxyImage(
    @Query('url') imageUrl: string,
    @Res() res: FastifyReply,
  ): Promise<void> {
    if (!imageUrl) {
      res.status(HttpStatus.BAD_REQUEST).send('Missing url parameter');
      return;
    }

    try {
      const backendUrl = this.config.getOrThrow<string>('agent.backendUrl');
      const gatewayKey = this.config.getOrThrow<string>('agent.gatewayKey');

      const target = `${backendUrl}/channels/whatsapp/media-proxy?url=${encodeURIComponent(imageUrl)}`;
      const response = await fetch(target, {
        headers: {
          'X-Gateway-Key': gatewayKey,
        },
      });

      if (!response.ok) {
        this.logger.error(`Media proxy upstream error: ${response.status}`);
        res.status(response.status).send('Image conversion failed');
        return;
      }

      const buffer = Buffer.from(await response.arrayBuffer());
      res
        .header('Content-Type', 'image/jpeg')
        .header('Cache-Control', 'public, max-age=604800, immutable')
        .status(HttpStatus.OK)
        .send(buffer);
    } catch (error) {
      this.logger.error(
        `Media proxy error: ${error instanceof Error ? error.message : String(error)}`,
      );
      res.status(HttpStatus.INTERNAL_SERVER_ERROR).send('Media proxy error');
    }
  }
}
