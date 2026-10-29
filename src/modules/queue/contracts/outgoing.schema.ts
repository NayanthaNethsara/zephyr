import { z } from 'zod';
import { interactiveSchema } from './interactive.schema';
import {
  traceIdSchema,
  correlationIdSchema,
} from '../../../lib/validation/telemetry';

const text = z.string().min(1);
const envelope = {
  traceId: traceIdSchema.optional(),
  correlationId: correlationIdSchema.optional(),
  expiresAt: z
    .number()
    .int()
    .positive()
    .max(Number.MAX_SAFE_INTEGER)
    .optional(),
  to: z.string().regex(/^\+?[1-9][0-9]{6,14}$/),
  phoneNumberId: z
    .string()
    .regex(/^[0-9]{1,50}$/)
    .optional(),
  replyToMessageId: text.optional(),
  messageId: text.optional(),
};
const media = z
  .object({
    mediaUrl: z.url().optional(),
    mediaId: text.optional(),
    caption: z.string().optional(),
    filename: text.optional(),
  })
  .refine(
    (value) => Boolean(value.mediaUrl || value.mediaId),
    'Media reference required',
  );

const message = <T extends string, S extends z.ZodType>(type: T, content: S) =>
  z.object({ ...envelope, type: z.literal(type), content });

export const outgoingMessageSchema = z.discriminatedUnion('type', [
  message(
    'text',
    z.object({ text: text.max(4096), previewUrl: z.boolean().optional() }),
  ),
  message('image', media),
  message('video', media),
  message('audio', media),
  message('sticker', media),
  message('document', media),
  message(
    'location',
    z.object({
      latitude: z.number().min(-90).max(90),
      longitude: z.number().min(-180).max(180),
      name: z.string().optional(),
      address: z.string().optional(),
    }),
  ),
  message(
    'contacts',
    z.object({
      contacts: z
        .array(
          z.object({
            name: z.object({
              formattedName: text,
              firstName: z.string().optional(),
              lastName: z.string().optional(),
            }),
            phones: z
              .array(
                z.object({
                  phone: text,
                  type: z.string().optional(),
                  waId: z.string().optional(),
                }),
              )
              .optional(),
            emails: z
              .array(z.object({ email: text, type: z.string().optional() }))
              .optional(),
            urls: z
              .array(z.object({ url: text, type: z.string().optional() }))
              .optional(),
            org: z
              .object({
                company: z.string().optional(),
                department: z.string().optional(),
                title: z.string().optional(),
              })
              .optional(),
          }),
        )
        .min(1),
    }),
  ),
  message('reaction', z.object({ messageId: text, emoji: z.string() })),
  message('interactive', z.object({ interactive: interactiveSchema })),
  message(
    'template',
    z.object({
      template: z.object({
        name: text,
        language: text,
        components: z
          .array(
            z.object({
              type: text,
              parameters: z.array(
                z
                  .object({
                    type: text,
                    text: z.string().optional(),
                    image: z.object({ link: z.url() }).optional(),
                    video: z.object({ link: z.url() }).optional(),
                    document: z.object({ link: z.url() }).optional(),
                  })
                  .passthrough(),
              ),
            }),
          )
          .optional(),
      }),
    }),
  ),
]);
