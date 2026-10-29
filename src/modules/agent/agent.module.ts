import { Module } from '@nestjs/common';
import { QueueModule } from '../queue/queue.module';
import { IncomingMessageConsumer } from './incoming-message-consumer';
import { InboxModule } from '../inbox/inbox.module';

@Module({
  imports: [QueueModule, InboxModule],
  providers: [IncomingMessageConsumer],
})
export class AgentModule {}
