import { Controller, Get, ServiceUnavailableException } from '@nestjs/common';
import { QueueService } from '../queue/queue.service';

@Controller()
export class HealthController {
  constructor(private readonly queue: QueueService) {}

  @Get('health')
  health() {
    return { status: 'ok', service: 'whatsapp-gateway' };
  }

  @Get('ready')
  async ready() {
    try {
      await this.queue.check();
      return {
        status: 'ok',
        service: 'whatsapp-gateway',
        queue: this.queue.provider,
      };
    } catch {
      throw new ServiceUnavailableException('Queue is unavailable');
    }
  }
}
