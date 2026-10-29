import { Module } from '@nestjs/common';
import { WebhookController } from './webhook.controller';
import { WebhookService } from './webhook.service';
import { WhatsAppModule } from '../whatsapp/whatsapp.module';
import { QueueModule } from '../queue/queue.module';

@Module({
  imports: [WhatsAppModule, QueueModule],
  controllers: [WebhookController],
  providers: [WebhookService],
  exports: [WebhookService],
})
export class WebhookModule {}
