import { Logger } from '@nestjs/common';
import { queueTelemetrySchema } from '../../lib/validation/telemetry';

export function queueTelemetry(payload: unknown) {
  const parsed = queueTelemetrySchema.safeParse(payload);
  return parsed.success
    ? {
        trace_id: parsed.data.traceId,
        correlation_id: parsed.data.correlationId ?? parsed.data.messageId,
        message_id: parsed.data.messageId,
      }
    : {};
}

export function event(
  logger: Logger,
  name: string,
  fields: Record<string, unknown> = {},
) {
  // Call sites supply IDs, statuses and timings only, never payloads or secrets.
  logger.log({ service: 'whatsapp-gateway', event: name, ...fields });
}
