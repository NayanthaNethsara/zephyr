import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { InboxRepository } from './inbox.repository';
import { InboxService } from './inbox.service';

@Module({
  imports: [ConfigModule],
  providers: [InboxRepository, InboxService],
  exports: [InboxService],
})
export class InboxModule {}
