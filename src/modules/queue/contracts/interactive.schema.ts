import { z } from 'zod';

const text = z.string().min(1);
const body = z.object({ text });
const urlAction = z.object({ displayText: text, url: z.url() });
const buttons = z
  .array(
    z.object({
      type: z.literal('reply'),
      reply: z.object({ id: text, title: text }),
    }),
  )
  .min(1)
  .max(3);
const mediaReference = {
  mediaUrl: z.url().nullish(),
  mediaId: text.nullish(),
};
const header = z
  .discriminatedUnion('type', [
    z.object({ type: z.literal('text'), text }),
    z.object({ type: z.literal('image'), ...mediaReference }),
    z.object({ type: z.literal('video'), ...mediaReference }),
    z.object({ type: z.literal('document'), ...mediaReference }),
  ])
  .refine(
    (value) =>
      value.type === 'text' || Boolean(value.mediaUrl || value.mediaId),
    'Header media reference required',
  );
const common = { body, footer: body.nullish(), header: header.nullish() };
const record = z.record(z.string(), z.unknown());
const card = z
  .object({
    header: z
      .object({ type: z.enum(['image', 'video']), ...mediaReference })
      .refine((value) => Boolean(value.mediaUrl || value.mediaId)),
    body: body.nullish(),
    footer: body.nullish(),
    ctaUrl: urlAction.nullish(),
    buttons: buttons.nullish(),
  })
  .refine(
    (value) => Boolean(value.ctaUrl) !== Boolean(value.buttons),
    'Choose a URL or reply buttons',
  );

export const interactiveSchema = z.discriminatedUnion('type', [
  z.object({
    type: z.literal('button'),
    ...common,
    action: z.object({ buttons }),
  }),
  z.object({
    type: z.literal('list'),
    ...common,
    action: z.object({
      button: text,
      sections: z
        .array(
          z.object({
            title: text.nullish(),
            rows: z
              .array(
                z.object({
                  id: text,
                  title: text,
                  description: z.string().nullish(),
                }),
              )
              .min(1),
          }),
        )
        .min(1),
    }),
  }),
  z.object({ type: z.literal('cta_url'), ...common, action: urlAction }),
  z.object({ type: z.literal('location_request_message'), body }),
  z.object({
    type: z.literal('carousel'),
    body,
    cards: z.array(card).min(1).max(10),
  }),
  z.object({
    type: z.literal('address_message'),
    body,
    footer: body.nullish(),
    action: z.object({
      country: text,
      values: record.nullish(),
      savedAddresses: z.array(z.object({ id: text, value: record })).nullish(),
    }),
  }),
  z.object({
    type: z.literal('flow'),
    ...common,
    action: z
      .object({
        cta: text,
        flowId: text.nullish(),
        flowName: text.nullish(),
        flowToken: text.nullish(),
        flowAction: z.enum(['navigate', 'data_exchange']).nullish(),
        screen: text.nullish(),
        data: record.nullish(),
        mode: z.enum(['draft', 'published']).nullish(),
      })
      .refine(
        (value) => Boolean(value.flowId) !== Boolean(value.flowName),
        'Choose a flow ID or flow name',
      ),
  }),
  z.object({ type: z.literal('call_permission_request'), body }),
]);
