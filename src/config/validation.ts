import * as Joi from 'joi';

const topic = Joi.string().pattern(/^[a-zA-Z0-9_-]+(?:\.[a-zA-Z0-9_-]+)*$/);

export const validationSchema = Joi.object({
  PORT: Joi.number().integer().min(1).max(65535).default(8080),
  META_VERIFY_TOKEN: Joi.string().required(),
  META_APP_SECRET: Joi.string().required(),
  WHATSAPP_PHONE_NUMBER_ID: Joi.string()
    .pattern(/^[0-9]{1,50}$/)
    .required(),
  WHATSAPP_ACCESS_TOKEN: Joi.string().required(),
  WHATSAPP_API_VERSION: Joi.string()
    .pattern(/^v\d+\.\d+$/)
    .default('v23.0'),
  WHATSAPP_REQUEST_TIMEOUT_MS: Joi.number().integer().min(1000).default(15000),
  WHATSAPP_MEDIA_SUPPORTED: Joi.boolean().default(false),
  WHATSAPP_TYPING_REFRESH_MS: Joi.number().integer().min(1000).default(20000),
  WHATSAPP_TYPING_MAX_MS: Joi.number().integer().min(1000).default(300000),
  AGENT_WORKER_ENABLED: Joi.boolean().default(true),
  AGENT_BACKEND_URL: Joi.string()
    .uri({ scheme: ['http', 'https'] })
    .default('http://127.0.0.1:8000'),
  EITRI_GATEWAY_KEY: Joi.when('AGENT_WORKER_ENABLED', {
    is: true,
    then: Joi.string().min(32).required(),
    otherwise: Joi.string().allow('').optional(),
  }),
  QUEUE_PROVIDER: Joi.string()
    .valid('redis', 'postgres', 'nats')
    .default('redis'),
  QUEUE_CONSUMER_GROUP: Joi.string()
    .pattern(/^[a-zA-Z0-9_-]+$/)
    .default('eitri-whatsapp-gateway'),
  QUEUE_LEASE_MS: Joi.number().integer().min(10000).default(60000),
  QUEUE_MAX_ATTEMPTS: Joi.number().integer().min(1).max(100).default(5),
  QUEUE_DEDUP_SECONDS: Joi.number().integer().min(60).default(86400),
  REDIS_URL: Joi.string()
    .uri({ scheme: ['redis', 'rediss'] })
    .default('redis://localhost:6379'),
  REDIS_QUEUE_PREFIX: Joi.string()
    .pattern(/^[a-zA-Z0-9:_-]+$/)
    .default('eitri:queue'),
  QUEUE_POSTGRES_URL: Joi.when('QUEUE_PROVIDER', {
    is: 'postgres',
    then: Joi.string()
      .uri({ scheme: ['postgres', 'postgresql'] })
      .required(),
    otherwise: Joi.string().allow('').optional(),
  }),
  NATS_URL: Joi.string().default('nats://localhost:4222'),
  NATS_STREAM_NAME: Joi.string()
    .pattern(/^[a-zA-Z0-9_-]+$/)
    .default('EITRI_WHATSAPP_EVENTS'),
  QUEUE_TOPIC_INCOMING_TEXT: topic,
  QUEUE_TOPIC_INCOMING_IMAGE: topic,
  QUEUE_TOPIC_INCOMING_VIDEO: topic,
  QUEUE_TOPIC_INCOMING_AUDIO: topic,
  QUEUE_TOPIC_INCOMING_DOCUMENT: topic,
  QUEUE_TOPIC_OUTGOING: topic,
  NATS_SUBJECT_INCOMING_TEXT: topic,
  NATS_SUBJECT_INCOMING_IMAGE: topic,
  NATS_SUBJECT_INCOMING_VIDEO: topic,
  NATS_SUBJECT_INCOMING_AUDIO: topic,
  NATS_SUBJECT_INCOMING_DOCUMENT: topic,
  NATS_SUBJECT_OUTGOING: topic,
});
