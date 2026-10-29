# Architecture

This document describes how Zephyr is built today (release 0.1, inbound only). The target design for later releases is in [wa-gateway-architecture.md](wa-gateway-architecture.md).

## What it does

Zephyr receives webhooks from Meta's WhatsApp Cloud API, proves they are genuine, saves them durably, and forwards them to your own HTTP endpoint with retries. It contains no bot logic and does not interpret message content.

## System context

```mermaid
flowchart LR
    subgraph Meta["Meta"]
        MW["Webhook sender"]
    end

    subgraph Host["Your server"]
        PROXY["TLS reverse proxy"]
        subgraph Z["Zephyr (single process)"]
            API["HTTP API"]
            WORKER["Outbox worker"]
            JAN["Janitor"]
            DB[("SQLite file")]
        end
    end

    subgraph You["Your systems"]
        APP["Your service"]
        PROM["Prometheus (optional)"]
    end

    MW -->|"POST /webhook"| PROXY --> API
    API --> DB
    WORKER <--> DB
    JAN --> DB
    WORKER -->|"signed POST"| APP
    PROM -->|"GET /metrics"| API
```

Everything runs in one process with one SQLite file. There is no separate queue, cache, or database server.

## Layers and dependency rules

The code is layer-based. Dependencies point inward, so the business rules never depend on HTTP, SQLite, or Meta specifics.

```mermaid
flowchart TB
    CMD["cmd/gateway<br/>wiring and lifecycle"]

    subgraph Delivery["Delivery and adapters"]
        API["api<br/>HTTP handlers, middleware"]
        META["meta<br/>signature check, payload parsing"]
        OUTBOX["outbox<br/>retry worker"]
        JANITOR["janitor<br/>cleanup"]
        TRANSPORT_IMPL["transport/webhook<br/>signed HTTP delivery"]
        STORE_IMPL["store/sqlite<br/>persistence"]
        TELEMETRY["telemetry<br/>Prometheus metrics"]
    end

    subgraph Contracts["Interface packages"]
        STORE["store<br/>Store interface"]
        TRANSPORT["transport<br/>Transport interface"]
    end

    CORE["core<br/>business rules"]
    CRYPTO["crypto<br/>HMAC primitives"]
    CONFIG["config<br/>environment loading"]

    CMD --> API
    CMD --> OUTBOX
    CMD --> JANITOR
    CMD --> TRANSPORT_IMPL
    CMD --> STORE_IMPL
    CMD --> TELEMETRY
    CMD --> CONFIG

    API --> CORE
    API --> META
    META --> CORE
    META --> CRYPTO
    CORE --> STORE
    OUTBOX --> STORE
    OUTBOX --> TRANSPORT
    JANITOR --> STORE
    STORE_IMPL -. implements .-> STORE
    TRANSPORT_IMPL -. implements .-> TRANSPORT
    TRANSPORT_IMPL --> CRYPTO
    TELEMETRY --> STORE
```

Rules enforced by convention (see `AGENT.md`):

- `core` imports only the interface package `store`, never `store/sqlite`.
- Only `cmd/gateway` creates concrete implementations and passes them in.
- `api` and `outbox` define the small recorder interfaces they need, and `telemetry` satisfies them. Neither imports Prometheus.

## Package map

| Package                       | Responsibility                                                                                      |
| ----------------------------- | --------------------------------------------------------------------------------------------------- |
| `cmd/gateway`                 | Loads config, builds everything, starts the server, worker and janitor, handles shutdown            |
| `internal/config`             | Reads environment variables over defaults and validates them                                        |
| `internal/api`                | Gin routes: `/webhook`, `/healthz`, `/readyz`, `/metrics`. Request IDs, request logging             |
| `internal/meta`               | Verifies Meta's handshake and `X-Hub-Signature-256`, turns Meta payloads into neutral events        |
| `internal/core`               | Wraps events in envelopes, assigns IDs and topics, calls the store, reports queued versus duplicate |
| `internal/store`              | The `Store` interface and its data types                                                            |
| `internal/store/sqlite`       | SQLite implementation: transactions, dedup, outbox queries, stats, migrations                       |
| `internal/transport`          | The `Transport` interface                                                                           |
| `internal/transport/webhook`  | Delivers an event as a signed HTTP POST to your endpoint                                            |
| `internal/outbox`             | Polls for due events, delivers them, retries with backoff, marks exhausted events failed            |
| `internal/janitor`            | Periodically deletes expired dedup keys and old failed events                                       |
| `internal/crypto`             | HMAC-SHA256 signing and constant-time verification                                                  |
| `internal/telemetry`          | Prometheus metrics and the outbox-depth collector                                                   |
| `migrations`                  | Embedded SQL migrations applied at startup                                                          |

## Runtime components

One process runs three long-lived activities, all stopped together on shutdown.

| Activity        | What it does                                                    | Cadence                      |
| --------------- | --------------------------------------------------------------- | ---------------------------- |
| HTTP server     | Serves Meta's webhooks and the health and metrics endpoints     | On demand                    |
| Outbox worker   | Delivers queued events to `WEBHOOK_URL`                         | Polls every 500 ms, batch 50 |
| Janitor         | Deletes expired dedup keys and failed events past retention     | Every 1 minute               |

The worker handles one event at a time. SQLite is opened with a single connection, so all database access is serialized and there is no write contention between the API and the worker.

## Inbound path

```mermaid
sequenceDiagram
    autonumber
    participant M as Meta
    participant A as API
    participant X as meta package
    participant C as core
    participant D as SQLite

    M->>A: POST /webhook + X-Hub-Signature-256
    A->>A: Read body (max 2 MiB)
    A->>X: VerifySignature(app secret, header, raw body)
    alt invalid
        A-->>M: 401
    else valid
        A->>X: ParseWebhook(body)
        alt malformed
            A-->>M: 400
        else parsed
            A->>C: Ingest(events)
            C->>D: One transaction: dedup keys + outbox rows
            alt store error
                A-->>M: 500 (Meta will retry)
            else saved
                A-->>M: 200
                A->>A: Log each event, record metrics
            end
        end
    end
```

The `200` is sent only after the transaction commits. If the process dies before that, Meta gets no acknowledgement and retries, so no event is lost. The dedup insert and the outbox insert share a transaction, so a failure cannot leave a message marked as seen without being queued.

## Delivery path and outbox lifecycle

```mermaid
sequenceDiagram
    autonumber
    participant W as Outbox worker
    participant D as SQLite
    participant T as Webhook transport
    participant U as Your service

    loop every 500 ms
        W->>D: ListDue(now, 50)
        D-->>W: pending rows ordered by created_at
        loop each row
            W->>T: Publish(payload)
            T->>T: Sign timestamp + "." + body
            T->>U: POST with X-Gateway-* headers
            alt 2xx
                W->>D: Delete row
            else failure and attempts left
                W->>D: Reschedule with backoff and jitter
            else attempts exhausted
                W->>D: Mark failed
            end
        end
    end
```

```mermaid
stateDiagram-v2
    [*] --> pending: event saved
    pending --> [*]: delivered (row deleted)
    pending --> pending: delivery failed, rescheduled
    pending --> failed: attempts exhausted
    failed --> [*]: removed by janitor after retention
```

Retry delay doubles each attempt from `RETRY_BASE_DELAY`, capped at 10 minutes, with jitter that keeps each delay between half and the full computed value. With the defaults (2 s base, 8 attempts) the seven waits between attempts add up to between about 2 and 4 minutes before the event is marked failed. Raise `RETRY_MAX_ATTEMPTS` or `RETRY_BASE_DELAY` to tolerate longer outages.

## Data model

```mermaid
erDiagram
    DEDUP {
        text dedup_key PK
        integer expires_at
    }
    OUTBOX {
        text id PK
        text topic
        text msg_key
        blob payload
        integer attempts
        integer next_attempt_at
        text state
        text last_error
        integer created_at
        integer updated_at
    }
```

The tables are not related by a foreign key. `dedup` remembers which Meta messages have been seen. `outbox` holds events waiting for delivery and, for a retention period, events that failed.

| Dedup key                 | Meaning                                              |
| ------------------------- | ---------------------------------------------------- |
| `msg:<wamid>`             | An inbound message, by its WhatsApp message ID       |
| `status:<wamid>:<status>` | One delivery status transition for an outgoing message |

A dedup key whose TTL has expired is treated as new, even before the janitor removes it.

## Guarantees

| Property                        | Behavior                                                                                             |
| ------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Acknowledge only after saving   | Meta receives `200` only after the event is committed to disk                                        |
| No loss on crash                | Queued events survive restarts and resume automatically                                              |
| Duplicate suppression           | Repeated webhooks for the same message ID within `DEDUP_TTL` are dropped                             |
| Delivery semantics              | At least once. A crash after your service answers but before the row is deleted causes a re-delivery  |
| Ordering                        | Not guaranteed. Retries reschedule events behind newer ones                                           |
| Backpressure                    | Unbounded queue on disk. A down service builds a backlog, visible in metrics                          |

## Security model

| Boundary                    | Control                                                                                         |
| --------------------------- | ----------------------------------------------------------------------------------------------- |
| Meta to Zephyr              | HMAC-SHA256 of the raw body with the app secret, constant-time compare, 2 MiB body limit        |
| Meta verification handshake | Constant-time verify token compare                                                              |
| Zephyr to your service      | HMAC over `timestamp.body` with your signing secret, timestamp for replay protection            |
| Data at rest                | SQLite file mode `0600`, payload kept only while queued or failed                               |
| Logs                        | No message content, tokens, or secrets. Phone numbers masked. Delivery errors omit the URL      |
| Container                   | Distroless base, non-root user, read-only root filesystem                                       |

## Design decisions

| Decision                          | Reason                                                                                              |
| --------------------------------- | --------------------------------------------------------------------------------------------------- |
| SQLite outbox instead of memory   | Survives crashes and restarts, needs no extra service, matches the self-hosting goal                |
| Acknowledge Meta after saving     | Meta's retries then cover any failure before the commit                                             |
| One transaction for dedup and queue | Prevents a message being marked seen but never queued                                              |
| Signed delivery with timestamp    | Lets the receiver reject forged and replayed requests                                               |
| Layered packages with interfaces  | Core logic stays testable and transports and databases can be swapped later                         |
| Single connection to SQLite       | Removes lock contention in a single-process design                                                  |
| Pure Go SQLite driver             | Keeps the binary static, no CGO, small distroless image                                             |
