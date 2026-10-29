import { Injectable } from '@nestjs/common';
import { randomUUID, createHash } from 'node:crypto';
import { incomingTextSchema } from '../agent/contracts/chat.schema';
import { InboxRepository } from './inbox.repository';
import { inboxBatchSchema, InboxBatch } from '../../lib/validation/inbox';
import { replyDeadline } from '../../lib/validation/message-age';

export type InboxClaim = {
  sender: string;
  owner: string;
  batch: InboxBatch;
  requestId: string;
  expiresAt: number;
};

@Injectable()
export class InboxService {
  constructor(private readonly repository: InboxRepository) {}

  private sender(business: string, phone: string) {
    return `${business}:${phone.replace(/^\+/, '')}`;
  }

  async enqueue(payload: unknown): Promise<boolean> {
    const message = incomingTextSchema.parse(payload);
    if (Date.now() >= replyDeadline(message.timestamp)) return true;
    return this.repository.enqueue(
      this.sender(message.to, message.from),
      message.messageId,
      JSON.stringify(message),
    );
  }

  async claim(business: string, phone: string): Promise<InboxClaim | null> {
    const sender = this.sender(business, phone);
    const owner = randomUUID();
    const raw = await this.repository.claim(sender, owner);
    if (!raw) return null;
    try {
      const batch = inboxBatchSchema.parse(JSON.parse(raw));
      if (
        batch.messages.some(
          (message) => this.sender(message.to, message.from) !== sender,
        )
      )
        throw new Error('Inbox sender mismatch');
      const requestId =
        batch.messages.length === 1
          ? batch.messages[0].messageId
          : `batch:${createHash('sha256')
              .update(
                JSON.stringify(
                  batch.messages.map((message) => message.messageId),
                ),
              )
              .digest('hex')}`;
      return {
        sender,
        owner,
        batch,
        requestId,
        expiresAt: Math.min(
          ...batch.messages.map((message) => replyDeadline(message.timestamp)),
        ),
      };
    } catch (error) {
      await this.repository.release(sender, owner);
      throw error;
    }
  }

  async assertOwned(claim: InboxClaim): Promise<void> {
    if (!(await this.repository.renew(claim.sender, claim.owner)))
      throw new Error('Inbox lease lost');
  }

  async complete(claim: InboxClaim): Promise<void> {
    if (!(await this.repository.complete(claim.sender, claim.owner)))
      throw new Error('Inbox lease lost');
  }

  release(claim: InboxClaim) {
    return this.repository.release(claim.sender, claim.owner);
  }
}
