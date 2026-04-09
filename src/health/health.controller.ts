import { Controller, Get, HttpCode, HttpStatus } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';

@Controller('health')
export class HealthController {
  constructor(private readonly configService: ConfigService) {}

  @Get()
  @HttpCode(HttpStatus.OK)
  checkHealth() {
    return {
      status: 'ok',
      timestamp: new Date().toISOString(),
      checks: {
        nats: {
          url: this.configService.get<string>('nats.url'),
          streamName: this.configService.get<string>('nats.streamName'),
          incomingTextSubject: this.configService.get<string>(
            'nats.subjects.incomingText',
          ),
          outgoingTextSubject: this.configService.get<string>(
            'nats.subjects.outgoingText',
          ),
        },
        meta: {
          appSecret: this.configService.get<string>('meta.appSecret')
            ? 'Set'
            : 'Missing',
          verifyToken: this.configService.get<string>('meta.verifyToken')
            ? 'Set'
            : 'Missing',
        },
      },
    };
  }
}
