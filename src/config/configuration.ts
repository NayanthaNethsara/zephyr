export default () => ({
  port: parseInt(process.env.PORT || '3000', 10),
  meta: {
    verifyToken: process.env.META_VERIFY_TOKEN,
    appSecret: process.env.META_APP_SECRET,
  },
  whatsapp: {
    phoneNumberId: process.env.WHATSAPP_PHONE_NUMBER_ID,
    accessToken: process.env.WHATSAPP_ACCESS_TOKEN,
    businessAccountId: process.env.WHATSAPP_BUSINESS_ACCOUNT_ID,
  },
  nats: {
    url: process.env.NATS_URL,
    streamName: process.env.NATS_STREAM_NAME || 'LEGALAID_EVENTS',
    subjects: {
      incomingText:
        process.env.NATS_SUBJECT_INCOMING_TEXT ||
        process.env.NATS_SUBJECT_INCOMING ||
        'whatsapp.incoming.text',
      outgoingText:
        process.env.NATS_SUBJECT_OUTGOING_TEXT ||
        process.env.NATS_SUBJECT_OUTGOING ||
        'whatsapp.outgoing.text',
    },
  },
});
