import { Module } from '@nestjs/common';
import { WhatsAppService } from './whatsapp.service';
import { TypingIndicatorService } from './typing-indicator.service';
import { MediaProxyController } from './media-proxy.controller';

@Module({
  controllers: [MediaProxyController],
  providers: [WhatsAppService, TypingIndicatorService],
  exports: [WhatsAppService, TypingIndicatorService],
})
export class WhatsAppModule {}
