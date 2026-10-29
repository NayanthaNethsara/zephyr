# WA Gateway — Architecture & Implementation Spec

> Working name: `Zephyr`. Open-source, self-hosted gateway for Meta's **official WhatsApp Cloud API**, written in Go.
> This document is the source of truth for implementation. When something is ambiguous, prefer the simplest option that keeps the principles in §2.

---

## 1. Overview

The gateway moves messages between **Meta's WhatsApp Cloud API** and the **user's systems** (bots, CRMs, workflows). It does not contain any business logic, AI, or bot behaviour. It verifies, deduplicates, rate-limits, retries and routes — nothing more.

| Item               | Value                                             |
| ------------------ | ------------------------------------------------- |
| Upstream           | Meta WhatsApp Cloud API (Graph API + webhooks)    |
| Downstream         | User's app via HTTP (default) or a message broker |
| Language           | Go                                                |
| Default deployment | Single binary + SQLite, no other services         |
| Message content    | Not stored by default (pass-through)              |

---

## 2. Principles

| #   | Principle             | Implication                                                                  |
| --- | --------------------- | ---------------------------------------------------------------------------- |
| P1  | Pure gateway          | No AI, bot logic, commerce, CRM, or flow builder                             |
| P2  | Official API only     | No WhatsApp Web / unofficial engines                                         |
| P3  | Pass-through          | Message content is only held temporarily (outbox) and deleted after delivery |
| P4  | Simple self-hosting   | Works with one binary + SQLite; brokers and Postgres are optional            |
| P5  | Pluggable transport   | `http` default; `nats`, `redis`, `rabbitmq`, `kafka` as adapters             |
| P6  | Extended from outside | Integrations happen via API and events, never in-process third-party plugins |
| P7  | API-first             | No dashboard in early phases; everything is doable via REST                  |

### Non-goals

- Running or hosting bots / LLMs
- Storing conversation history (except an optional, off-by-default module later)
- Supporting WhatsApp Web, Baileys, whatsmeow, etc.
- Being a BSP (no billing, no Embedded Signup in v0.x)

---

## 3. Architecture

### 3.1 System diagram

```mermaid
flowchart LR
    subgraph META["Meta WhatsApp Cloud API"]
        MWH["Webhooks"]
        MAPI["Graph API<br/>/{phone_number_id}/messages"]
    end

    subgraph GW["wa-gateway (Go binary)"]
        direction TB
        WR["Webhook Receiver<br/>verify handshake + HMAC"]
        DD["Deduplicator<br/>(wamid)"]
        SS["Session Tracker<br/>(24h window)"]
        EP["Event Publisher"]

        API["REST API (Gin)<br/>/v1/*"]
        OC["Outbound Consumer"]
        IK["Idempotency Check"]
        WC["Window Check"]
        RL["Rate Limiter<br/>(per number)"]
        SND["Sender<br/>(Meta client)"]

        OB["Outbox + Retry Worker"]
        TR{{"Transport Adapter<br/>http | nats | redis | rabbitmq | kafka"}}
    end

    subgraph STORE["Storage"]
        DB[("SQLite (default)<br/>or Postgres")]
    end

    subgraph USER["User's systems"]
        APP["Bot / CRM / Workflow"]
        BRK[["Broker (optional)"]]
    end

    MWH -->|POST event| WR --> DD --> SS --> EP --> TR
    TR -->|webhook POST / publish| APP
    TR -.-> BRK -.-> APP

    APP -->|POST /v1/messages| API --> IK
    BRK -.->|wa.outbound| OC --> IK
    IK --> WC --> RL --> SND -->|HTTPS| MAPI
    SND -->|result| EP

    EP --> OB
    OB --> TR
    SND -. retry .-> OB

    DD --- DB
    SS --- DB
    IK --- DB
    OB --- DB
```

### 3.2 Inbound flow (Meta → user)

```mermaid
sequenceDiagram
    autonumber
    participant M as Meta
    participant G as Gateway
    participant D as DB
    participant T as Transport
    participant U as User App

    M->>G: POST /webhook (X-Hub-Signature-256)
    G->>G: Verify HMAC with app secret
    alt invalid signature
        G-->>M: 401
    else valid
        G->>D: Insert wamid into dedup (TTL)
        alt duplicate
            G-->>M: 200 (ignored)
        else new
            G->>D: Upsert session window (hashed phone, last_inbound_at)
            G->>D: Insert outbox row (event)
            G-->>M: 200 (fast ack)
            G->>T: Deliver event (inbound / status)
            alt delivered
                T->>U: event
                G->>D: Delete outbox row
            else failed
                G->>D: attempts++, next_attempt_at = backoff
            end
        end
    end
```

> Always ack Meta quickly (after persisting to outbox). Never block the webhook response on downstream delivery.

### 3.3 Outbound flow (user → Meta)

```mermaid
sequenceDiagram
    autonumber
    participant U as User App
    participant G as Gateway
    participant D as DB
    participant M as Meta
    participant T as Transport

    U->>G: Send request (REST or wa.outbound)
    G->>D: Check idempotency key
    alt seen before
        G-->>U: Return stored result
    else new
        G->>D: Check session window
        alt free-form outside 24h
            G-->>U: 422 window_closed (no send)
        else allowed
            G->>G: Rate limit (per phone_number_id)
            G->>M: POST /{version}/{phone_number_id}/messages
            alt 2xx
                M-->>G: wamid
                G->>D: Store idempotency result (TTL)
                G-->>U: 200 {wamid} (sync) / 202 (async)
                G->>T: result event (accepted)
            else retryable (429, 5xx, timeout)
                G->>D: Outbox / Nack with delay
            else permanent (4xx)
                G-->>U: error
                G->>T: result event (rejected)
            end
        end
    end
```

### 3.4 Transport modes

```mermaid
flowchart TB
    subgraph HTTP["TRANSPORT=http (default)"]
        H1["Inbound: signed webhook POST to user URL"]
        H2["Outbound: REST API"]
        H3["Retries: DB outbox"]
    end
    subgraph BROKER["TRANSPORT=nats | redis | rabbitmq | kafka"]
        B1["Inbound: publish to wa.inbound / wa.status"]
        B2["Outbound: consume wa.outbound + REST API"]
        B3["Results: publish to wa.results"]
        B4["Retries: broker redelivery with delay; DLQ wa.dlq"]
    end
```

---

## 4. Components

| Package              | Responsibility                                                                                                                      |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `internal/meta`      | Graph API client, webhook handshake (`hub.mode`, `hub.verify_token`, `hub.challenge`), HMAC verification, Meta error classification |
| `internal/core`      | Pipeline: dedup, session window, idempotency, rate limiting, routing                                                                |
| `internal/api`       | Gin REST handlers, auth middleware, request validation                                                                              |
| `internal/transport` | `Transport` interface + adapters (`http`, `nats`, `redis`, `rabbitmq`, `kafka`)                                                     |
| `internal/outbox`    | Durable outbox, retry worker, backoff, dead-letter, replay                                                                          |
| `internal/store`     | Storage interface + `sqlite` and `postgres` implementations (sqlc)                                                                  |
| `internal/config`    | koanf loading: env → optional YAML file                                                                                             |
| `internal/crypto`    | Token encryption at rest, phone hashing, HMAC signing                                                                               |
| `internal/janitor`   | TTL cleanup (dedup, idempotency, sessions, delivered outbox rows)                                                                   |
| `cmd/gateway`        | Entry point, wiring, graceful shutdown                                                                                              |

---

## 5. Transport abstraction

### 5.1 Interfaces

```go
type Transport interface {
    Name() string
    Publish(ctx context.Context, topic string, key string, payload []byte) error
    Subscribe(ctx context.Context, topic string, h Handler) error
    Close() error
}

type Handler func(ctx context.Context, m Message) error

type Message interface {
    Data() []byte
    Key() string
    Attempt() int
    Ack() error
    Nack(delay time.Duration) error // retry later
    Term() error                    // give up → DLQ
}
```

- `key` = recipient phone (for outbound) or sender phone (for inbound). Adapters use it for ordering/partitioning.
- The `http` adapter implements `Publish` as a signed webhook POST via the outbox; `Subscribe` is a no-op (outbound comes from the REST API).

### 5.2 Topics

| Topic         | Direction      | Payload                          |
| ------------- | -------------- | -------------------------------- |
| `wa.inbound`  | Gateway → user | Incoming message events          |
| `wa.status`   | Gateway → user | sent / delivered / read / failed |
| `wa.outbound` | User → gateway | Send requests                    |
| `wa.results`  | Gateway → user | Send outcome (accepted/rejected) |
| `wa.dlq`      | Gateway → user | Messages that exhausted retries  |

Topic prefix is configurable (`TRANSPORT_TOPIC_PREFIX`, default `wa`).

### 5.3 Adapter notes

| Adapter    | Library               | Ordering                 | Delayed retry               | DLQ                         |
| ---------- | --------------------- | ------------------------ | --------------------------- | --------------------------- |
| `http`     | net/http              | n/a                      | DB outbox `next_attempt_at` | `failed` state + replay API |
| `nats`     | `nats.go` (JetStream) | subject per key hash     | `NakWithDelay`              | publish to `wa.dlq`         |
| `redis`    | `go-redis` (Streams)  | stream per key hash      | delayed ZSET re-queue       | `wa.dlq` stream             |
| `rabbitmq` | `amqp091-go`          | consistent-hash exchange | TTL retry queue + DLX       | DLX queue                   |
| `kafka`    | `franz-go`            | partition by key         | retry topic(s)              | `wa.dlq` topic              |

Each adapter is behind a Go **build tag** (`nats`, `redis`, `rabbitmq`, `kafka`) so users can build slim binaries. Default build includes `http` + `nats`.

---

## 6. Message contracts

### 6.1 Event envelope (gateway → user)

```json
{
  "id": "evt_01J...",
  "type": "message.inbound",
  "occurred_at": "2026-10-25T10:15:00Z",
  "phone_number_id": "123456789",
  "waba_id": "987654321",
  "data": {}
}
```

| `type`            | `data`                                                                                  |
| ----------------- | --------------------------------------------------------------------------------------- |
| `message.inbound` | Normalised inbound message + original Meta payload under `raw`                          |
| `message.status`  | `{ wamid, status, recipient, timestamp, errors? }`                                      |
| `message.result`  | `{ correlation_id, idempotency_key, status: "accepted" \| "rejected", wamid?, error? }` |

HTTP delivery headers:

| Header                | Value                                            |
| --------------------- | ------------------------------------------------ |
| `X-Gateway-Event`     | event type                                       |
| `X-Gateway-Delivery`  | event id                                         |
| `X-Gateway-Signature` | `sha256=<HMAC(WEBHOOK_SIGNING_SECRET, body)>`    |
| `X-Gateway-Timestamp` | unix seconds (include in HMAC to prevent replay) |

### 6.2 Outbound request (user → gateway)

```json
{
  "correlation_id": "order-1234",
  "idempotency_key": "order-1234-confirm",
  "phone_number_id": "123456789",
  "to": "9477xxxxxxx",
  "type": "template",
  "template": {
    "name": "order_confirm",
    "language": "en",
    "components": []
  }
}
```

- `type` mirrors Meta types: `text`, `image`, `video`, `audio`, `document`, `sticker`, `location`, `contacts`, `interactive`, `template`, `reaction`.
- The body for each type follows Meta's shape; the gateway validates required fields and passes it through.
- `phone_number_id` optional when only one number is configured.
- Implementation note: the REST API takes the idempotency key from the `Idempotency-Key` header only, claims it before sending (so concurrent duplicates cannot both send), rejects the same key with a different message, and stores only successful sends. See [api.md](api.md) and [architecture.md](architecture.md).

### 6.3 Error object

```json
{
  "code": "window_closed",
  "message": "Free-form messages require an open 24h session",
  "retryable": false,
  "meta_code": 131047
}
```

---

## 7. REST API (v1)

All endpoints require `Authorization: Bearer <api_key>` except the Meta webhook and health.

| Method   | Path                     | Purpose                                    | Phase |
| -------- | ------------------------ | ------------------------------------------ | ----- |
| GET      | `/webhook`               | Meta verification handshake                | v0.1  |
| POST     | `/webhook`               | Meta events                                | v0.1  |
| POST     | `/v1/messages`           | Send message (sync default, `?async=true`) | v0.1  |
| GET      | `/v1/messages/{id}`      | Async send status                          | v0.1  |
| GET      | `/v1/sessions/{phone}`   | Is the 24h window open?                    | v0.1  |
| GET      | `/v1/events/failed`      | List dead-lettered events (HTTP mode)      | v0.1  |
| POST     | `/v1/events/{id}/replay` | Replay a failed event                      | v0.1  |
| GET      | `/healthz` / `/readyz`   | Liveness / readiness                       | v0.1  |
| CRUD     | `/v1/admin/numbers`      | Manage phone numbers + tokens              | v0.2  |
| CRUD     | `/v1/admin/api-keys`     | Manage API keys                            | v0.2  |
| CRUD     | `/v1/admin/webhooks`     | Manage downstream webhook URLs             | v0.2  |
| GET/POST | `/v1/templates`          | List / sync templates                      | v0.3  |
| POST     | `/v1/media`              | Upload media to Meta                       | v0.3  |
| GET      | `/metrics`               | Prometheus                                 | v0.3  |

Headers: `Idempotency-Key` (optional, recommended), `X-Correlation-Id` (optional, echoed back).

OpenAPI spec lives at `api/openapi.yaml` and is served at `/docs`.

---

## 8. Data model

No message content outside `outbox.payload`, which is deleted after delivery.

```mermaid
erDiagram
    PHONE_NUMBERS {
        string id PK "phone_number_id"
        string waba_id
        string display_number
        bytes access_token_enc
        string app_secret_ref
        datetime created_at
    }
    API_KEYS {
        string id PK
        string name
        string key_hash
        string scopes
        datetime created_at
        datetime revoked_at
    }
    WEBHOOK_TARGETS {
        string id PK
        string url
        string event_types
        bool active
    }
    DEDUP {
        string dedup_key PK
        datetime expires_at
    }
    IDEMPOTENCY {
        string key PK
        string result_json
        datetime expires_at
    }
    SESSIONS {
        string phone_hash PK
        string phone_number_id PK
        datetime last_inbound_at
    }
    OUTBOX {
        string id PK
        string kind "event | send"
        string topic
        string msg_key
        bytes payload
        int attempts
        datetime next_attempt_at
        string state "pending | failed"
        string last_error
    }
    PHONE_NUMBERS ||--o{ SESSIONS : has
```

| Table         | Retention                                                              |
| ------------- | ---------------------------------------------------------------------- |
| `dedup`       | `DEDUP_TTL` (default 48h)                                              |
| `idempotency` | `IDEMPOTENCY_TTL` (default 24h)                                        |
| `sessions`    | 24h after `last_inbound_at`                                            |
| `outbox`      | Deleted on success; `failed` rows kept `FAILED_RETENTION` (default 7d) |

Phone numbers in `sessions` are stored as `HMAC(PHONE_HASH_KEY, phone)`.

---

## 9. Configuration

Precedence: **env > YAML file (`CONFIG_FILE`) > defaults**.

| Variable                 | Default                     | Description                                      |
| ------------------------ | --------------------------- | ------------------------------------------------ |
| `GATEWAY_PORT`           | `8080`                      | HTTP port                                        |
| `LOG_LEVEL`              | `info`                      | `debug` / `info` / `warn` / `error`              |
| `DB_URL`                 | `sqlite:///data/gateway.db` | SQLite or `postgres://...`                       |
| `ENCRYPTION_KEY`         | — (required)                | 32-byte key for tokens at rest                   |
| `PHONE_HASH_KEY`         | — (required)                | Key for hashing phone numbers                    |
| `META_API_VERSION`       | `v24.0`                     | Graph API version                                |
| `META_APP_SECRET`        | —                           | For webhook HMAC verification                    |
| `META_VERIFY_TOKEN`      | —                           | For webhook handshake                            |
| `WA_PHONE_NUMBER_ID`     | —                           | Single-number mode                               |
| `WA_WABA_ID`             | —                           | Single-number mode                               |
| `WA_ACCESS_TOKEN`        | —                           | Single-number mode                               |
| `API_KEYS`               | —                           | Comma-separated bootstrap keys                   |
| `TRANSPORT`              | `http`                      | `http` / `nats` / `redis` / `rabbitmq` / `kafka` |
| `TRANSPORT_TOPIC_PREFIX` | `wa`                        | Topic prefix                                     |
| `WEBHOOK_URL`            | —                           | Downstream URL (http mode)                       |
| `WEBHOOK_SIGNING_SECRET` | —                           | Signs downstream webhooks                        |
| `NATS_URL`               | —                           | e.g. `nats://nats:4222`                          |
| `REDIS_URL`              | —                           | e.g. `redis://redis:6379`                        |
| `RABBITMQ_URL`           | —                           | e.g. `amqp://guest:guest@rabbitmq:5672/`         |
| `KAFKA_BROKERS`          | —                           | Comma-separated                                  |
| `RETRY_MAX_ATTEMPTS`     | `8`                         | Before dead-letter                               |
| `RETRY_BASE_DELAY`       | `2s`                        | Exponential backoff base (with jitter)           |
| `RATE_LIMIT_MPS`         | `80`                        | Per phone number                                 |
| `DEDUP_TTL`              | `48h`                       |                                                  |
| `IDEMPOTENCY_TTL`        | `24h`                       |                                                  |
| `FAILED_RETENTION`       | `168h`                      |                                                  |

---

## 10. Reliability & error handling

| Situation                                              | Behaviour                                                                       |
| ------------------------------------------------------ | ------------------------------------------------------------------------------- |
| Meta webhook received                                  | Persist to outbox, then ack 200 fast                                            |
| Duplicate webhook                                      | Ack 200, drop                                                                   |
| Downstream delivery fails                              | Exponential backoff with jitter up to `RETRY_MAX_ATTEMPTS`, then `failed` / DLQ |
| Meta 429 / throughput errors                           | Retry with backoff; respect rate limiter                                        |
| Meta 5xx / timeout                                     | Retry                                                                           |
| Meta 4xx (invalid number, bad template, window closed) | No retry; return error + `message.result` rejected                              |
| Free-form send outside window                          | Reject before calling Meta (`window_closed`)                                    |
| Process crash                                          | Outbox rows survive; worker resumes on start                                    |
| Shutdown                                               | Stop accepting, drain in-flight sends, close transports (graceful, timeout 30s) |

Meta error codes must be mapped to `retryable: true/false` in `internal/meta/errors.go` (table-driven, easy to extend).

---

## 11. Security

| Area                | Requirement                                                                                        |
| ------------------- | -------------------------------------------------------------------------------------------------- |
| Meta webhook        | Verify `X-Hub-Signature-256` (HMAC-SHA256 of raw body with app secret) using constant-time compare |
| Downstream webhooks | Sign with `WEBHOOK_SIGNING_SECRET`, include timestamp                                              |
| API keys            | Store only hashes; compare in constant time                                                        |
| Tokens              | Encrypt access tokens at rest (AES-GCM with `ENCRYPTION_KEY`)                                      |
| Phones              | Hash in session table                                                                              |
| Logs                | Never log message content, tokens, or full phone numbers (mask)                                    |
| Container           | Non-root, read-only root FS, minimal image (distroless/scratch)                                    |

---

## 12. Tech stack

| Layer      | Choice                                                          |
| ---------- | --------------------------------------------------------------- |
| Language   | Go (latest stable)                                              |
| HTTP       | Gin (+ Huma for OpenAPI, optional)                              |
| DB driver  | `modernc.org/sqlite` (CGO-free) / `pgx`                         |
| Queries    | `sqlc` (separate query sets for SQLite and Postgres if needed)  |
| Migrations | `goose` (embedded)                                              |
| Config     | `koanf`                                                         |
| Logging    | `log/slog` (JSON)                                               |
| Metrics    | `prometheus/client_golang`                                      |
| Brokers    | `nats.go`, `go-redis/v9`, `amqp091-go`, `franz-go`              |
| Testing    | `testing` + `testify`, `testcontainers-go` for brokers/Postgres |
| Lint       | `golangci-lint`                                                 |
| Release    | GoReleaser (binaries + Docker images)                           |

---

## 13. Repository layout

```
zephyr/
├── apps/
│   └── gateway/            # Go service (dashboard can live in apps/dashboard later)
│       ├── cmd/gateway/main.go
│       ├── internal/
│       │   ├── api/        # Gin handlers, middleware
│       │   ├── config/
│       │   ├── core/       # dedup, session, idempotency, rate limit, pipeline
│       │   ├── crypto/
│       │   ├── janitor/
│       │   ├── meta/       # client, webhook verify, errors
│       │   ├── outbox/
│       │   ├── store/
│       │   │   ├── sqlite/
│       │   │   └── postgres/
│       │   └── transport/
│       │       ├── transport.go
│       │       ├── webhook/
│       │       ├── nats/
│       │       ├── redis/
│       │       ├── rabbitmq/
│       │       └── kafka/
│       ├── migrations/
│       ├── api/openapi.yaml
│       └── Dockerfile
├── docker-compose.yml
├── docs/
│   ├── meta-setup.md
│   ├── transports.md
│   ├── events.md
│   └── compliance.md
├── Makefile
└── README.md
```

---

## 14. Roadmap & acceptance criteria

```mermaid
gantt
    dateFormat  YYYY-MM-DD
    title Roadmap (indicative)
    section Core
    v0.1 HTTP + NATS core        :a1, 2026-11-01, 30d
    v0.2 Admin API, Redis, RabbitMQ :a2, after a1, 21d
    v0.3 Kafka, templates, media, metrics :a3, after a2, 21d
    v1.0 Stable API + docs        :a4, after a3, 14d
    section Later
    Dashboard + SDKs              :a5, after a4, 30d
```

### v0.1 — Core

- [ ] Meta webhook handshake + HMAC verification
- [ ] Inbound dedup by `wamid`
- [ ] Session window tracking + `window_closed` rejection
- [ ] `POST /v1/messages` (sync + async) for all message types
- [ ] Idempotency keys
- [ ] Per-number rate limiter
- [ ] Outbox with backoff, dead-letter, replay endpoint
- [ ] `http` transport (signed webhooks) and `nats` transport (all topics)
- [ ] API key auth (bootstrap via env)
- [ ] SQLite store + migrations, janitor TTL cleanup
- [ ] Health endpoints, graceful shutdown
- [ ] Docker image + compose examples
- **Done when:** a message sent to the WhatsApp number reaches a test webhook/NATS consumer exactly once, and a reply sent via REST or `wa.outbound` arrives on the phone, surviving a gateway restart mid-retry.

### v0.2

- [ ] Admin API (numbers, API keys, webhook targets), multi-number
- [ ] Postgres store
- [ ] `redis` and `rabbitmq` transports

### v0.3

- [ ] `kafka` transport
- [ ] Template list/sync, media upload
- [ ] Prometheus metrics

### v1.0

- [ ] Frozen v1 API + OpenAPI
- [ ] Full docs (Meta setup, transports, events, compliance)
- [ ] Load test at configured MPS

---

## 15. Implementation guidelines (for agents/contributors)

1. Keep the core transport-agnostic: `internal/core` must not import any adapter package.
2. Every external call (Meta, brokers, webhooks) takes a `context.Context` with timeout.
3. Ack Meta webhooks only after the event is durably in the outbox.
4. All retries use exponential backoff with jitter; no unbounded retries.
5. No message content in logs, metrics labels, or errors.
6. Store interface first, then SQLite implementation, then Postgres.
7. Table-driven tests for Meta error classification, signature verification, and window logic.
8. Integration tests per transport via `testcontainers-go`, behind build tags.
9. Public API changes must update `api/openapi.yaml` in the same change.
10. Prefer standard library over new dependencies.

---

## 16. Compliance note (for README)

This project connects to Meta's official WhatsApp Business Platform. Operators are responsible for complying with Meta's WhatsApp Business Messaging Policy and Business Terms, including opt-in requirements, template rules, and the restriction (effective 15 January 2026) on general-purpose AI assistants being the primary functionality offered via the Business Platform. The gateway does not inspect or enforce what users connect behind it.
