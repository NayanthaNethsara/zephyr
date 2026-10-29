import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { WhatsAppModule } from '../whatsapp/whatsapp.module';
import { IncomingProducer } from './incoming-producer';
import { OutgoingMessageConsumer } from './outgoing-message-consumer';
import { QueueService } from './queue.service';
import { OutgoingProducer } from './outgoing-producer';
import { InboxModule } from '../inbox/inbox.module';

@Module({
  imports: [ConfigModule, WhatsAppModule, InboxModule],
  providers: [
    QueueService,
    IncomingProducer,
    OutgoingProducer,
    OutgoingMessageConsumer,
  ],
  exports: [QueueService, IncomingProducer, OutgoingProducer],
})
export class QueueModule {}
