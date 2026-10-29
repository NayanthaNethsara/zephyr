export default () => ({
  port: parseInt(process.env.PORT || '8080', 10),
  meta: {
    verifyToken: process.env.META_VERIFY_TOKEN,
    appSecret: process.env.META_APP_SECRET,
  },
  whatsapp: {
    phoneNumberId: process.env.WHATSAPP_PHONE_NUMBER_ID,
    accessToken: process.env.WHATSAPP_ACCESS_TOKEN,
    publicBaseUrl:
      process.env.WHATSAPP_PUBLIC_URL ||
      'https://keren-awesome-daubingly.ngrok-free.dev',
    apiVersion: process.env.WHATSAPP_API_VERSION || 'v23.0',
    requestTimeoutMs: Number(process.env.WHATSAPP_REQUEST_TIMEOUT_MS || 15000),
    mediaSupported: process.env.WHATSAPP_MEDIA_SUPPORTED === 'true',
    typing: {
      refreshMs: parseInt(
        process.env.WHATSAPP_TYPING_REFRESH_MS || '20000',
        10,
      ),
      maxMs: parseInt(process.env.WHATSAPP_TYPING_MAX_MS || '300000', 10),
    },
  },
  agent: {
    enabled: process.env.AGENT_WORKER_ENABLED !== 'false',
    backendUrl: (
      process.env.AGENT_BACKEND_URL || 'http://127.0.0.1:8000'
    ).replace(/\/$/, ''),
    gatewayKey: process.env.EITRI_GATEWAY_KEY,
  },
  queue: {
    provider: process.env.QUEUE_PROVIDER || 'redis',
    consumerGroup: process.env.QUEUE_CONSUMER_GROUP || 'eitri-whatsapp-gateway',
    leaseMs: Number(process.env.QUEUE_LEASE_MS || 60000),
    maxAttempts: Number(process.env.QUEUE_MAX_ATTEMPTS || 5),
    dedupSeconds: Number(process.env.QUEUE_DEDUP_SECONDS || 86400),
    redisUrl: process.env.REDIS_URL || 'redis://localhost:6379',
    redisPrefix: process.env.REDIS_QUEUE_PREFIX || 'eitri:queue',
    postgresUrl: process.env.QUEUE_POSTGRES_URL,
    natsUrl: process.env.NATS_URL || 'nats://localhost:4222',
    natsStream: process.env.NATS_STREAM_NAME || 'EITRI_WHATSAPP_EVENTS',
    topics: {
      incomingText:
        process.env.QUEUE_TOPIC_INCOMING_TEXT ||
        process.env.NATS_SUBJECT_INCOMING_TEXT ||
        'whatsapp.incoming.text',
      incomingImage:
        process.env.QUEUE_TOPIC_INCOMING_IMAGE ||
        process.env.NATS_SUBJECT_INCOMING_IMAGE ||
        'whatsapp.incoming.media.image',
      incomingVideo:
        process.env.QUEUE_TOPIC_INCOMING_VIDEO ||
        process.env.NATS_SUBJECT_INCOMING_VIDEO ||
        'whatsapp.incoming.media.video',
      incomingAudio:
        process.env.QUEUE_TOPIC_INCOMING_AUDIO ||
        process.env.NATS_SUBJECT_INCOMING_AUDIO ||
        'whatsapp.incoming.media.audio',
      incomingDocument:
        process.env.QUEUE_TOPIC_INCOMING_DOCUMENT ||
        process.env.NATS_SUBJECT_INCOMING_DOCUMENT ||
        'whatsapp.incoming.media.document',
      outgoing:
        process.env.QUEUE_TOPIC_OUTGOING ||
        process.env.NATS_SUBJECT_OUTGOING ||
        'whatsapp.outgoing',
    },
  },
});
