import { z } from 'zod';

export const traceIdSchema = z.uuid();
export const correlationIdSchema = z
  .string()
  .regex(/^[A-Za-z0-9._:+=/-]{1,300}$/);
export const queueTelemetrySchema = z.object({
  traceId: traceIdSchema.optional(),
  correlationId: correlationIdSchema.optional(),
  messageId: correlationIdSchema.optional(),
});
