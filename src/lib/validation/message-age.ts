import { z } from 'zod';

export const REPLY_MAX_AGE_MS = 20 * 60 * 60 * 1000;
export const incomingTimestampSchema = z
  .string()
  .regex(/^[1-9]\d{0,10}$/)
  .transform(Number)
  .refine((seconds) => Number.isSafeInteger(seconds * 1000));

export function replyDeadline(timestamp: string): number {
  const seconds = incomingTimestampSchema.parse(timestamp);
  // Reject timestamps in the future rather than extending the reply window.
  if (seconds * 1000 > Date.now() + 60_000)
    throw new Error('Invalid future message timestamp');
  return Math.min(seconds * 1000, Date.now()) + REPLY_MAX_AGE_MS;
}
