import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { NatsService } from './nats.service';
import { IncomingMessageProducer } from './incoming-message-producer';
import { OutgoingMessageConsumer } from './outgoing-message-consumer';
import { WhatsAppModule } from '../whatsapp/whatsapp.module';

@Module({
  imports: [ConfigModule, WhatsAppModule],
  providers: [
    NatsService,
    IncomingMessageProducer,
    OutgoingMessageConsumer,
  ],
  exports: [IncomingMessageProducer],
})
export class NatsModule {}
