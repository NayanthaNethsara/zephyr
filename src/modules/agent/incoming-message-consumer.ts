import { Injectable, Logger, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { randomUUID } from 'node:crypto';
import { event } from '../../common/utils/telemetry';
import { incomingTextSchema, agentReplySchema } from './contracts/chat.schema';
import { QueueService } from '../queue/queue.service';
import { InvalidQueueMessage } from '../queue/queue.types';
import { OutgoingProducer } from '../queue/outgoing-producer';
import { PermanentDeliveryError } from '../../common/errors/permanent-delivery.error';
import { retryAfterSchema } from '../../lib/validation/quota';
import { replyDeadline } from '../../lib/validation/message-age';
import { InboxService, InboxClaim } from '../inbox/inbox.service';

@Injectable()
export class IncomingMessageConsumer implements OnModuleInit {
  private readonly logger = new Logger(IncomingMessageConsumer.name);

  constructor(
    private readonly queue: QueueService,
    private readonly outgoing: OutgoingProducer,
    private readonly config: ConfigService,
    private readonly inbox: InboxService,
  ) {}

  async onModuleInit(): Promise<void> {
    if (!this.config.getOrThrow<boolean>('agent.enabled')) return;
    await this.queue.subscribe(
      this.config.getOrThrow<string>('queue.topics.incomingText'),
      async (payload) => {
        const parsed = incomingTextSchema.safeParse(payload);
        if (!parsed.success)
          throw new InvalidQueueMessage('Invalid incoming text contract');
        const message = parsed.data;
        if (
          message.to !==
          this.config.getOrThrow<string>('whatsapp.phoneNumberId')
        )
          throw new InvalidQueueMessage(
            'Message belongs to a different business number',
          );
        // Also handles messages already queued before the inbox was deployed.
        if (
          Date.now() < replyDeadline(message.timestamp) &&
          !(await this.inbox.enqueue(message))
        )
          throw new PermanentDeliveryError('Sender inbox is full');
        let continuationId = '';
        for (let index = 0; index < 5; index++) {
          const claim = await this.inbox.claim(message.to, message.from);
          if (!claim) return; // Another replica owns the sender, or the inbox is empty.
          await this.process(claim);
          continuationId = claim.requestId;
        }
        // Yield the queue so one busy sender cannot hold it indefinitely.
        await this.queue.publish(
          this.config.getOrThrow<string>('queue.topics.incomingText'),
          message,
          `drain:${continuationId}`,
        );
      },
    );
  }

  private async notice(
    claim: InboxClaim,
    kind: string,
    text: string,
  ): Promise<void> {
    await this.inbox.assertOwned(claim);
    const last = claim.batch.messages.at(-1)!;
    await this.outgoing.publish({
      traceId: claim.batch.messages[0].traceId,
      correlationId: claim.requestId,
      messageId: `${last.to}:${claim.requestId}:${kind}`,
      phoneNumberId: last.to,
      to: last.from,
      type: 'text',
      expiresAt: claim.expiresAt,
      content: { text },
    });
  }

  private async deadLetter(claim: InboxClaim, reason: string): Promise<void> {
    const topic = this.config.getOrThrow<string>('queue.topics.incomingText');
    await this.queue.publish(
      `${topic}.dead`,
      {
        source: topic,
        messageId: claim.requestId,
        body: JSON.stringify(claim.batch.messages),
        attempts: claim.batch.attempts,
        reason,
        failedAt: new Date().toISOString(),
      },
      `dead:${claim.requestId}`,
    );
  }

  private async process(claim: InboxClaim): Promise<void> {
    const started = Date.now();
    const requestId = randomUUID();
    const traceId = claim.batch.messages[0].traceId ?? randomUUID();
    claim.batch.messages[0].traceId = traceId;
    const telemetry = {
      request_id: requestId,
      trace_id: traceId,
      correlation_id: claim.requestId,
      attempt: claim.batch.attempts,
      batch_size: claim.batch.messages.length,
      source_message_ids: claim.batch.messages.map(
        (message) => message.messageId,
      ),
      source_trace_ids: [
        ...new Set(
          claim.batch.messages
            .map((message) => message.traceId)
            .filter(Boolean),
        ),
      ],
    };
    event(this.logger, 'batch.started', telemetry);
    let outcomePublished = false;
    const cancelled = new AbortController();
    const heartbeat = setInterval(() => {
      void this.inbox.assertOwned(claim).catch(() => cancelled.abort());
    }, 20_000);
    try {
      if (Date.now() >= claim.expiresAt) {
        event(this.logger, 'batch.expired', telemetry);
        await this.inbox.complete(claim);
        return;
      }
      const maxAttempts = this.config.getOrThrow<number>('queue.maxAttempts');
      if (claim.batch.attempts > maxAttempts) {
        await this.notice(
          claim,
          'unavailable',
          'The assistant is unavailable right now. Please try sending your message again later.',
        );
        outcomePublished = true;
        await this.deadLetter(claim, 'Processing attempts exhausted');
        await this.inbox.complete(claim);
        return;
      }
      const first = claim.batch.messages[0];
      const last = claim.batch.messages.at(-1)!;
      const combined = claim.batch.messages
        .map((message) => message.text)
        .join('\n\n');
      const response = await fetch(
        `${this.config.getOrThrow<string>('agent.backendUrl')}/channels/whatsapp/chat`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'X-Gateway-Key': this.config.getOrThrow<string>('agent.gatewayKey'),
            'X-Request-ID': requestId,
            'X-Trace-ID': traceId,
            'X-Correlation-ID': claim.requestId,
          },
          body: JSON.stringify({
            message_id: claim.requestId,
            received_at: new Date(
              Math.min(
                ...claim.batch.messages.map(
                  (message) => Number(message.timestamp) * 1000,
                ),
              ),
            ).toISOString(),
            phone_number: first.from,
            business_number_id: first.to,
            message: combined,
            input_metadata: {
              source: claim.batch.messages.length === 1 ? first.source : 'text',
              selection_id:
                claim.batch.messages.length === 1 ? first.replyId : null,
              reply_to_message_id: first.context?.messageId ?? null,
            },
          }),
          signal: AbortSignal.any([
            cancelled.signal,
            AbortSignal.timeout(
              Math.max(1, Math.min(200000, claim.expiresAt - Date.now())),
            ),
          ]),
        },
      );
      event(this.logger, 'backend.completed', {
        ...telemetry,
        status: response.status,
        duration_ms: Date.now() - started,
      });
      await this.inbox.assertOwned(claim);
      if (Date.now() >= claim.expiresAt) {
        await response.body?.cancel();
        await this.inbox.complete(claim);
        return;
      }
      if (!response.ok) {
        await response.body?.cancel();
        if (response.status === 410) {
          event(this.logger, 'batch.expired', {
            ...telemetry,
            source: 'backend',
          });
          await this.inbox.complete(claim);
          return;
        }
        if (response.status === 429) {
          event(this.logger, 'quota.rejected', telemetry);
          const minutes = Math.ceil(
            retryAfterSchema.parse(response.headers.get('Retry-After')) / 60,
          );
          await this.notice(
            claim,
            'quota',
            `The assistant has reached a usage limit. Please send your message again in about ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}.`,
          );
          outcomePublished = true;
          this.logger.warn(
            'WhatsApp quota reached; batch acknowledged without AI retry',
          );
          await this.inbox.complete(claim);
          return;
        }
        if ([400, 401, 403, 404, 422].includes(response.status))
          throw new PermanentDeliveryError(
            `Agent backend rejected request (HTTP ${response.status})`,
          );
        throw new Error(`Agent backend unavailable (HTTP ${response.status})`);
      }
      const reply = agentReplySchema.parse(await response.json());
      if (reply.request_id !== claim.requestId)
        throw new Error('Backend reply does not match batch');
      await this.inbox.assertOwned(claim);
      if (reply.messages && reply.messages.length > 0) {
        for (let index = 0; index < reply.messages.length; index++) {
          const outgoingMessage = reply.messages[index];
          if (outgoingMessage.type === 'interactive') {
            await this.outgoing.publish({
              traceId,
              correlationId: claim.requestId,
              messageId: `${last.to}:${claim.requestId}:reply:${index}`,
              phoneNumberId: last.to,
              to: last.from,
              type: 'interactive',
              content: outgoingMessage.content,
              expiresAt: claim.expiresAt,
            });
          } else {
            await this.outgoing.publish({
              traceId,
              correlationId: claim.requestId,
              messageId: `${last.to}:${claim.requestId}:reply:${index}`,
              phoneNumberId: last.to,
              to: last.from,
              type: 'text',
              content: outgoingMessage.content,
              expiresAt: claim.expiresAt,
            });
          }
        }
      } else {
        await this.outgoing.publish({
          traceId,
          correlationId: claim.requestId,
          messageId: `${last.to}:${claim.requestId}:reply`,
          phoneNumberId: last.to,
          to: last.from,
          type: 'text',
          content: { text: reply.reply },
          expiresAt: claim.expiresAt,
        });
      }
      outcomePublished = true;
      await this.inbox.complete(claim);
    } catch (error) {
      event(this.logger, 'batch.failed', {
        ...telemetry,
        error_type: error instanceof Error ? error.name : 'UnknownError',
        error_message: error instanceof Error ? error.message : String(error),
      });
      // Never publish a response after losing the sender lease.
      if (cancelled.signal.aborted) throw new Error('Sender lease lost');
      await this.inbox.assertOwned(claim);
      // A cleanup failure after durable publication must not send a contradictory notice.
      if (outcomePublished) {
        await this.inbox.complete(claim);
        return;
      }
      if (Date.now() >= claim.expiresAt) {
        await this.inbox.complete(claim);
        return;
      }
      if (
        error instanceof PermanentDeliveryError ||
        claim.batch.attempts >=
          this.config.getOrThrow<number>('queue.maxAttempts')
      ) {
        await this.notice(
          claim,
          'unavailable',
          'The assistant is unavailable right now. Please try sending your message again later.',
        );
        await this.deadLetter(
          claim,
          error instanceof PermanentDeliveryError
            ? error.message
            : 'Processing attempts exhausted',
        );
        await this.inbox.complete(claim);
        return;
      }
      throw error;
    } finally {
      event(this.logger, 'batch.finished', {
        ...telemetry,
        outcome_queued: outcomePublished,
        duration_ms: Date.now() - started,
      });
      clearInterval(heartbeat);
      // Release only our own lease; keep the active batch intact on transient errors.
      await this.inbox.release(claim);
    }
  }
}
