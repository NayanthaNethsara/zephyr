import { z } from 'zod';
import { incomingTextSchema } from '../../modules/agent/contracts/chat.schema';

export const inboxBatchSchema = z.object({
  messages: z.array(incomingTextSchema).min(1).max(20),
  attempts: z.number().int().positive(),
});

export type InboxBatch = z.infer<typeof inboxBatchSchema>;
