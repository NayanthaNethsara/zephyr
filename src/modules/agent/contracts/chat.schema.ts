import { z } from 'zod';
import { incomingTimestampSchema } from '../../../lib/validation/message-age';
import { traceIdSchema } from '../../../lib/validation/telemetry';
import { interactiveSchema } from '../../queue/contracts/interactive.schema';

const phone = z.string().regex(/^\+?[1-9][0-9]{6,14}$/);
const businessNumber = z.string().regex(/^[0-9]{1,50}$/);
const messageId = z.string().min(1).max(150);

export const incomingTextSchema = z
  .object({
    type: z.literal('text'),
    traceId: traceIdSchema.optional(),
    messageId,
    timestamp: z
      .string()
      .refine((value) => incomingTimestampSchema.safeParse(value).success),
    from: phone.transform((value) => value.replace(/^\+/, '')),
    to: businessNumber,
    text: z.string().trim().min(1).max(4096),
    source: z
      .enum(['text', 'button_reply', 'list_reply', 'quick_reply'])
      .default('text'),
    replyId: z.string().trim().min(1).max(1024).nullable().default(null),
    context: z
      .object({ messageId, from: z.string().min(1).optional() })
      .nullable()
      .default(null),
    metadata: z.object({ phoneNumberId: businessNumber }).optional(),
  })
  .superRefine((value, ctx) => {
    if (value.metadata && value.metadata.phoneNumberId !== value.to)
      ctx.addIssue({ code: 'custom', message: 'Business number mismatch' });
    if (value.source === 'text' && value.replyId !== null)
      ctx.addIssue({
        code: 'custom',
        message: 'Plain text cannot carry selection ID',
      });
    if (
      (value.source === 'button_reply' || value.source === 'list_reply') &&
      value.replyId === null
    )
      ctx.addIssue({ code: 'custom', message: 'Selection ID required' });
  });

export const agentOutgoingMessageSchema = z.discriminatedUnion('type', [
  z.object({
    type: z.literal('text'),
    content: z.object({ text: z.string().min(1) }),
  }),
  z.object({
    type: z.literal('interactive'),
    content: z.object({ interactive: interactiveSchema }),
  }),
]);

export const agentReplySchema = z.object({
  reply: z.string().min(1).max(4096),
  products: z.array(
    z.object({
      sku: z.string().min(1).max(100),
      name: z.string().min(1).max(500),
    }),
  ),
  channel: z.literal('whatsapp'),
  user_id: z.uuid(),
  conversation_id: z.uuid(),
  turn_id: z.uuid(),
  request_id: messageId,
  expires_at: z.null().or(z.string()).nullish(),
  messages: z.array(agentOutgoingMessageSchema).optional().default([]),
});
