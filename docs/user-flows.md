# User flows

Each flow shows what happens, who acts, and what you can observe in logs. Log messages quoted here are the real ones Zephyr emits. For event field details see [events.md](events.md).

## Flow 1: Operator setup

```mermaid
flowchart TD
    A["Create Meta app and add WhatsApp"] --> B["Copy app secret"]
    B --> C["Generate verify token and signing secret"]
    C --> D["Write .env"]
    D --> E["Start Zephyr"]
    E --> F{"Zephyr starts?"}
    F -- "no" --> G["Fix the config error it prints"] --> E
    F -- "yes" --> H["Expose /webhook over HTTPS"]
    H --> I["Enter callback URL and verify token in Meta"]
    I --> J{"Meta verification passes?"}
    J -- "no" --> K["Check token and tunnel"] --> I
    J -- "yes" --> L["Subscribe to the messages field"]
    L --> M["Send a test message"]
```

See [getting-started.md](getting-started.md) for the detailed steps.

## Flow 2: Meta verifies the webhook

```mermaid
sequenceDiagram
    participant Op as Operator
    participant M as Meta
    participant Z as Zephyr

    Op->>M: Save callback URL + verify token
    M->>Z: GET /webhook?hub.mode=subscribe&hub.verify_token=...&hub.challenge=123
    alt token matches
        Z-->>M: 200 with body 123
        Note over Z: log "webhook verified by meta"
        M-->>Op: Verified
    else token wrong
        Z-->>M: 403
        Note over Z: log "webhook verification rejected"
        M-->>Op: Verification failed
    end
```

## Flow 3: A customer sends a message

```mermaid
sequenceDiagram
    participant P as Customer
    participant M as Meta
    participant Z as Zephyr
    participant U as Your service

    P->>M: WhatsApp message
    M->>Z: POST /webhook (signed)
    Z->>Z: Verify signature, parse, save to disk
    Z-->>M: 200
    Note over Z: log "event received" outcome=queued
    Z->>U: POST event (message.inbound, signed)
    U-->>Z: 200
    Note over Z: log "event delivered"
```

Your service receives a `message.inbound` event containing the sender, message ID, timestamp, type, and the original Meta message under `raw`. Reply logic belongs in your service. Zephyr does not send replies in this release.

## Flow 4: A delivery status update

When a message sent from the business number is delivered, read, or fails, Meta sends a status webhook. Zephyr forwards it as a `message.status` event with `status` set to `sent`, `delivered`, `read`, or `failed`. Failed statuses include Meta's `errors`. The path through Zephyr is the same as Flow 3.

## Flow 5: Meta sends the same webhook twice

```mermaid
sequenceDiagram
    participant M as Meta
    participant Z as Zephyr
    participant D as SQLite

    M->>Z: POST /webhook (message A)
    Z->>D: dedup key msg:A not seen, queue it
    Z-->>M: 200
    M->>Z: POST /webhook (message A again)
    Z->>D: dedup key msg:A already seen, skip
    Z-->>M: 200
    Note over Z: log "event received" outcome=duplicate<br/>metric events_ingested_total outcome=duplicate
```

Meta always receives `200`, so it stops retrying. Your service receives the event once.

## Flow 6: Your service is down

```mermaid
flowchart TD
    A["Event is queued"] --> B["Worker POSTs to your service"]
    B --> C{"2xx response?"}
    C -- "yes" --> D["Delete from queue, log 'event delivered'"]
    C -- "no" --> E{"Attempts left?"}
    E -- "yes" --> F["Wait with exponential backoff and jitter<br/>log 'event delivery failed, will retry'"]
    F --> B
    E -- "no" --> G["Mark failed, keep for FAILED_RETENTION<br/>log 'event delivery exhausted'"]
```

While your service is down, Meta still gets `200` because the events are safely on disk. They pile up as pending entries. Watch `zephyr_outbox_entries{state="pending"}` and `zephyr_outbox_oldest_pending_age_seconds`. When your service returns, the backlog drains in arrival order, subject to the ordering caveat in [limitations.md](limitations.md).

## Flow 7: Zephyr restarts or crashes

```mermaid
sequenceDiagram
    participant Z as Zephyr
    participant D as SQLite
    participant U as Your service

    Note over Z,D: Event saved, delivery failing
    Z--xZ: process stops (deploy, crash, power loss)
    Note over D: pending rows remain on disk
    Z->>D: starts, applies migrations
    Z->>D: ListDue
    D-->>Z: the pending event
    Z->>U: delivery retried
    U-->>Z: 200
```

No manual recovery is needed. Keep the data volume (`/data` in Docker) on persistent storage, otherwise queued events are lost with the container.

## Flow 8: Meta cannot reach Zephyr

If Zephyr is stopped or the database fails during a webhook, Meta does not receive `200` and retries on its own schedule. When Zephyr returns, the retried webhooks are accepted and deduplicated. If the database is unavailable but the process is up, `/readyz` returns 503 and webhook requests answer 500, which is the signal for Meta to retry.

## Flow 9: How Zephyr answers each webhook request

```mermaid
flowchart TD
    A["POST /webhook"] --> B{"Body over 2 MiB?"}
    B -- "yes" --> R413["413"]
    B -- "no" --> C{"Valid signature?"}
    C -- "no" --> R401["401"]
    C -- "yes" --> D{"Parses as Meta payload?"}
    D -- "no" --> R400["400"]
    D -- "yes" --> E{"Any messages or statuses?"}
    E -- "no" --> R200a["200 (ignored)"]
    E -- "yes" --> F{"Saved to disk?"}
    F -- "no" --> R500["500, Meta retries"]
    F -- "yes" --> R200b["200"]
```

Every outcome is counted in `zephyr_webhook_requests_total{result=...}`.

## Flow 10: Handling a failed event

Events that exhaust their attempts stay in the outbox as `failed` for `FAILED_RETENTION` (7 days by default) and then are deleted. This release has no API to list or replay them. To inspect them, query the database:

```sh
sqlite3 data/gateway.db \
  "SELECT id, topic, attempts, last_error, datetime(updated_at/1000,'unixepoch') FROM outbox_entries WHERE state='failed'"
```

To retry a failed event, set it back to pending:

```sh
sqlite3 data/gateway.db \
  "UPDATE outbox_entries SET state='pending', attempts=0, next_attempt_at=0 WHERE id='evt_...'"
```

Do this with Zephyr stopped, or accept that it runs against a live database in WAL mode. A proper list and replay API is planned.

## Flow 11: Graceful shutdown

On `SIGTERM` or `Ctrl+C`, Zephyr logs `shutting down`, stops accepting new requests, lets in-flight requests finish (up to 30 seconds), stops the worker and janitor, then closes the database. An event whose delivery is interrupted stays pending and is retried on the next start.

## Flow 12: Tracing one message end to end

Take the `wamid` from your service or from Meta, then follow the `event_id`:

1. Search logs for `"wamid":"<wamid>"` and find `event received`. It shows the `request_id`, `event_id`, and whether it was `queued` or `duplicate`.
2. Search logs for the `event_id` to see every delivery attempt, its error, and finally `event delivered` with the time it took.
3. Use `request_id` to find the matching `request` log line with status and duration. The same ID is returned to the caller in the `X-Request-Id` response header.

See [telemetry.md](telemetry.md) for the full field list and metrics.
