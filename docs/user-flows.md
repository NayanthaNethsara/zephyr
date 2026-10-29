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

Your service receives a `message.inbound` event containing the sender, message ID, timestamp, type, and the original Meta message under `raw`. Reply logic belongs in your service, which can answer through the sending API (flow 13).

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

## Flow 13: Your service sends a message

```mermaid
sequenceDiagram
    participant U as Your service
    participant Z as Zephyr
    participant M as Meta
    participant P as Customer

    U->>Z: POST /v1/messages (API key, correlation id)
    Z->>Z: Check key, validate message
    Z->>M: Graph API send
    M-->>Z: wamid
    Z-->>U: 200 {"wamid": "..."}
    Note over Z: log "message sent" with wamid, type, masked number
    M->>P: Message delivered to the phone
    M->>Z: POST /webhook status: sent, delivered, read
    Z->>U: message.status events, signed
```

The `200` means Meta accepted the message, not that the phone received it. Delivery progress comes back through the normal inbound path as `message.status` events carrying the same `wamid`, so your service can match them to the send. A common pattern is a reply to an inbound message: take the sender from the `message.inbound` event's `data.from` and send within 24 hours.

## Flow 14: A send fails

```mermaid
flowchart TD
    A["POST /v1/messages"] --> B{"Valid API key?"}
    B -- "no" --> R401["401 unauthorized"]
    B -- "yes" --> C{"Body valid?"}
    C -- "no" --> R400["400 invalid_request, fix and resend"]
    C -- "yes" --> D["Zephyr calls Meta"]
    D --> E{"Meta answer"}
    E -- "accepted" --> R200["200 with wamid"]
    E -- "24-hour window closed" --> R422a["422 window_closed, send a template instead"]
    E -- "recipient problem" --> R422b["422 recipient_undeliverable or recipient_not_allowed"]
    E -- "template problem" --> R422c["422 template_error"]
    E -- "throttled" --> R429["429 rate_limited, retryable"]
    E -- "token or number problem" --> R502a["502 upstream_auth_failed or sender_misconfigured, fix configuration"]
    E -- "Meta down" --> R502b["502 upstream_unavailable, retryable"]
    E -- "no answer in 15 s" --> R504["504 upstream_timeout, retryable but may have been sent"]
```

Retry only when `retryable` is true. With an `Idempotency-Key` the retry is safe (flow 16). Without one, be careful with `upstream_timeout`: Meta may have accepted the message even though no answer arrived, so a blind retry can send it twice. Every failure is logged with the Meta error code and its trace ID, which Meta support can use. See [api.md](api.md) for the full table.

## Flow 15: Reaching a customer after 24 hours

WhatsApp lets you send free-form messages only within 24 hours of the customer's last message. After that, Meta rejects the send and Zephyr returns `422 window_closed`. Your service responds by sending an approved template message instead:

```mermaid
flowchart LR
    A["Send free-form text"] --> B{"window_closed?"}
    B -- "no" --> C["Delivered"]
    B -- "yes" --> D["Send approved template"]
    D --> E["Customer replies"]
    E --> F["Window reopens for 24 hours"]
```

Zephyr does not track the window itself, so the first rejected send is how your service learns it is closed. Your service can also track the time of the last `message.inbound` event per customer to avoid the failed call.

## Flow 16: Retrying a send safely

Your service sends an order confirmation and the connection drops before the response arrives. It cannot tell whether the customer was messaged.

```mermaid
sequenceDiagram
    participant U as Your service
    participant Z as Zephyr
    participant M as Meta

    U->>Z: POST /v1/messages, Idempotency-Key: order-1-confirm
    Z->>Z: Claim key order-1-confirm
    Z->>M: Send
    M-->>Z: wamid
    Z->>Z: Record wamid against the key
    Z--xU: response lost
    U->>Z: Retry, same Idempotency-Key and same message
    Z-->>U: 200 same wamid, Idempotent-Replayed: true
    Note over Z,M: The retry never reached Meta, the customer got one message
```

What your service sees on a retry depends on how the first attempt ended:

| First attempt                                  | Retry with the same key                                              |
| ---------------------------------------------- | -------------------------------------------------------------------- |
| Succeeded, response lost                       | `200` with the original `wamid` and `Idempotent-Replayed: true`      |
| Still running (slow Meta call)                 | `409 request_in_progress`. Wait a moment and retry again             |
| Failed with a Meta error                       | The key was released, so the retry sends for real and gets a fresh answer |
| Never reached Zephyr                           | Sends normally, since the key was never claimed                      |

Use one key per logical message and a different key for each new message. Reusing a key for a different message returns `422 idempotency_key_reused` so a bug on your side cannot silently drop a message.
