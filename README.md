# WhatsApp gateway

NestJS/Fastify gateway for signed WhatsApp webhooks and queued outbound delivery. Node.js 22 or later is required.

## Run locally

```sh
pnpm install
cp .env.example .env
docker compose up --detach redis   # or --profile nats
pnpm dev
```

Set real Meta and WhatsApp credentials in the gateway `.env`. Set a random `EITRI_GATEWAY_KEY` of at least 32 characters in `.env`; the Python backend and gateway must use the same value. `AGENT_BACKEND_URL` defaults to `http://127.0.0.1:8000`; `AGENT_WORKER_ENABLED=false` disables the incoming text worker if another integration consumes that topic. The gateway loads `.env` from the working directory; exported environment variables take precedence. It listens on port 8080. `GET /health` checks the process and returns `{ status, service }`. `GET /ready` checks the selected queue and returns 503 when unavailable. HTTP and asynchronous processing emit structured JSON logs with trace and correlation IDs; there is no metrics endpoint.

The webhook URL is `/whatsapp/webhooks`. Verification uses `META_VERIFY_TOKEN`; inbound POSTs require the original request bytes and a valid `sha256=` signature made with `META_APP_SECRET`. A successful webhook response means the supported messages were persisted to the queue. Queue failures return 503 so Meta can retry. Delivery-status webhooks are logged.

## Select the queue

Only the selected transport connects at startup. Change the provider and restart every producer/consumer using the queue.

| `QUEUE_PROVIDER`  | Connection                                                           | Implementation                                            |
| ----------------- | -------------------------------------------------------------------- | --------------------------------------------------------- |
| `redis` (default) | `REDIS_URL=redis://localhost:6379`                                   | Redis Streams, consumer groups, pending recovery          |
| `postgres`        | `QUEUE_POSTGRES_URL=postgresql://user:password@localhost:5432/eitri` | SQL queue with atomic leases and `FOR UPDATE SKIP LOCKED` |
| `nats`            | `NATS_URL=nats://localhost:4222`                                     | JetStream file storage and durable pull consumers         |

PostgreSQL creates the dedicated `gateway_queue_messages` table and pending index on startup. The connection user needs permission to create them; the inventory schema is separate. URL-encode credentials in connection URLs. NATS defaults to the new `EITRI_WHATSAPP_EVENTS` stream; set `NATS_STREAM_NAME` to reuse an existing compatible stream. Legacy `NATS_SUBJECT_*` overrides still work; new `QUEUE_TOPIC_*` settings take precedence.

All transports preserve the same JSON message DTOs and logical topic names. Switching providers does not migrate existing messages. Drain or explicitly migrate queued work before switching. The gateway's `agent/` module consumes incoming text, calls the Python backend's `/channels/whatsapp/chat`, and publishes a stable outgoing reply before acknowledging the incoming message. The same integration works with all three transports. PostgreSQL in the backend persists WhatsApp identities and history regardless of the selected queue.

## Structure

```text
src/
  config/                  Environment loading and validation
  common/                  Logging, filters, permanent delivery error
  modules/
    agent/                 Incoming text worker and Python conversation adapter
    webhook/               Signature verification and inbound normalization
    whatsapp/              Cloud API client, wire mapping, typing indicator
    queue/
      contracts/           Transport-neutral DTOs and outgoing validation
      incoming-producer.ts Inbound publication with stable deduplication key
      outgoing-producer.ts Validated outgoing publication
      outgoing-message-consumer.ts  WhatsApp delivery and telemetry
      queue.service.ts     Shared lifecycle, retries, and dead-letter handling
      queue.types.ts       Transport and subscription interfaces
      queue-worker.ts      Polling loop and graceful shutdown
      transports/          Factory plus Redis, PostgreSQL, and NATS adapters
    health/                Process and selected-queue readiness
```

Webhook and WhatsApp code depend on the shared queue service, not individual broker clients. Adding another transport means implementing `QueueTransport` and registering it in the factory and settings. The gateway handles one delivery at a time per replica. Multiple replicas share the consumer group or durable consumer and compete for work. `QUEUE_CONSUMER_GROUP` must be the same across gateway replicas; it identifies the worker fleet on each topic.

## Delivery behavior

`QUEUE_LEASE_MS` defaults to 60000. Workers renew active leases and recover abandoned deliveries after the lease expires. Successful delivery is acknowledged; transient failures stay queued for retry. `QUEUE_MAX_ATTEMPTS` defaults to 5. Invalid payloads and permanent WhatsApp HTTP failures go directly to `<topic>.dead`; exhausted retries go there too. The source is acknowledged only after the dead-letter publication succeeds. WhatsApp network failures, timeouts, HTTP 408, 429, and server errors are retryable. The API client has a bounded request timeout and leaves retries to the queue worker.

Incoming publications use the business number and WhatsApp message ID for deduplication. Outgoing publishers should use a stable `messageId` when retrying publication. `QUEUE_DEDUP_SECONDS` defaults to 86400: Redis keeps dedup keys for that duration, PostgreSQL retains completed rows for that duration after completion, and NATS uses it as the stream duplicate window. PostgreSQL cleanup runs in bounded batches. Redis removes completed entries; each topic is a work queue with one logical consumer group, not a broadcast log. Pending work and dead letters are retained until handled or explicitly removed. Monitor backlog, storage, and dead letters; do not use an eviction policy that silently discards queue data.

Delivery is at least once. A process failure after Meta accepts a send and before queue acknowledgment can cause a duplicate WhatsApp send; Meta's send API is outside the queue transaction. Retries and multiple replicas can reorder deliveries, so strict webhook arrival order is not guaranteed. The backend leases each conversation to prevent overlapping graph turns and returns 409 for busy conversations; the queue retries those deliveries. Completed turns are cached by WhatsApp message ID, so retries reuse the saved reply. Typing indicators are best effort and local to a gateway replica; outgoing delivery does not depend on them.

Unsupported incoming formats generate a queued text fallback. Media forwarding remains opt-in through `WHATSAPP_MEDIA_SUPPORTED`; bytes are fetched by the downstream worker, not the gateway. `WHATSAPP_API_VERSION` defaults to the existing `v23.0`; the version is configurable. `WHATSAPP_REQUEST_TIMEOUT_MS` defaults to 15000.

## Docker

Copy and fill `.env`, then run:

```sh
docker compose up --build --detach --wait
```

Redis uses a persistent volume, AOF, and `noeviction`. For NATS, set `QUEUE_PROVIDER=nats` in `.env` and run with `--profile nats` including the `nats` service. For PostgreSQL, point `QUEUE_POSTGRES_URL` at your own database. Compose overrides the Redis and NATS URLs to their service hostnames. The gateway image runs as a non-root user. Queue data lives in the queue services' persistent volumes. The Compose file does not include the Python agent backend; set `AGENT_BACKEND_URL` to wherever it runs, or `AGENT_WORKER_ENABLED=false`.

## Checks and contracts

```sh
pnpm typecheck
pnpm lint
pnpm build
```

- [Incoming message contract](docs/incoming-queues.md)
- [Outgoing message contract](docs/outgoing-queue.md)
- [Meta webhook shapes](docs/whatsapp-webhook.md)
- [Queue implementation references](docs/queue-transports.md)
