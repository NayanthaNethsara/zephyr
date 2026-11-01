# Telemetry

Zephyr gives you two views of what is happening: structured logs to follow a single message, and Prometheus metrics to watch the system as a whole. Neither contains message content, tokens, or full phone numbers.

## What is never recorded

- Message text, media files, or any field from the Meta message body, including the body of messages you send
- File names and file content of uploaded and downloaded media. Only the media ID, MIME type, and byte count are logged
- `WA_ACCESS_TOKEN` and your API keys. A rejected key is logged without its value
- `META_APP_SECRET`, `WEBHOOK_SIGNING_SECRET`, `META_VERIFY_TOKEN`
- Full phone numbers. They appear masked as `9477*****00` (first four and last two digits)
- The `WEBHOOK_URL`. Delivery errors are logged without it

## Logs

Logs are JSON, one object per line, written to stdout. Set the level with `LOG_LEVEL`. At `info`, health, readiness, and metrics requests are quiet. They appear at `debug`.

### Fields

| Field          | Appears on                          | Meaning                                                        |
| -------------- | ----------------------------------- | -------------------------------------------------------------- |
| `request_id`   | Webhook and request lines           | `req_` ID. Also returned to the caller as `X-Request-Id`       |
| `event_id`     | Event and delivery lines            | `evt_` ID. The key that follows a message through delivery     |
| `wamid`        | `event received`                    | Meta's message ID                                              |
| `type`         | `event received`                    | `message.inbound` or `message.status`                          |
| `subtype`      | `event received`                    | Message type (`text`, `image`, ...) or status (`delivered`, `read`, `failed`) |
| `phone`        | `event received`                    | Masked sender or recipient number                              |
| `outcome`      | `event received`                    | `queued` or `duplicate`                                        |
| `attempts`     | Delivery lines                      | Attempt number                                                 |
| `duration_ms`  | `event delivered`, `request`        | How long the delivery attempt or request took                  |
| `lag_ms`       | `event delivered`                   | Time from acceptance to successful delivery                    |
| `retry_in_ms`  | `event delivery failed`             | Delay before the next attempt                                  |
| `error`        | Failure lines                       | Reason, for example `webhook responded with status 500`        |
| `to`           | Send lines                          | Masked recipient number                                        |
| `correlation_id` | Send lines                        | Your reference from the request body or `X-Correlation-Id`     |
| `idempotent`   | Send lines                          | `true` when the request carried an `Idempotency-Key`           |
| `media_id`     | Media lines                         | Meta's media ID. Not sensitive, and never the file name or content |
| `bytes`        | Media lines                         | Bytes streamed so far or in total                              |
| `operation`    | Media failure lines                 | `upload` or `download`                                         |
| `code`         | `message send failed`               | Zephyr error code such as `window_closed`                      |
| `meta_code`    | `message send failed`               | Meta's numeric error code, `0` when the error did not come from Meta |
| `trace_id`     | `message send failed`               | Meta's `fbtrace_id`. Quote it to Meta support                   |

### Message lifecycle log lines

| `msg`                                   | Level | Meaning                                                      |
| --------------------------------------- | ----- | ------------------------------------------------------------ |
| `webhook verified by meta`              | info  | Handshake succeeded                                          |
| `webhook verification rejected`         | warn  | Handshake token did not match                                |
| `webhook signature rejected`            | warn  | Request was not signed by Meta                               |
| `webhook payload malformed`             | warn  | Signed but unparsable. Rejected with `400`                   |
| `webhook body rejected`                 | warn  | Body too large or unreadable                                 |
| `persist webhook events failed`         | error | Could not save to disk. Meta will retry                      |
| `event received`                        | info  | One line per event in a webhook, `queued` or `duplicate`     |
| `request`                               | info  | One line per HTTP request with route, status, duration       |
| `event delivered`                       | info  | Your service accepted the event                              |
| `event delivery failed, will retry`     | warn  | Attempt failed, next retry scheduled                         |
| `event delivery exhausted`              | error | Attempts used up. Event marked failed                        |
| `api key rejected`                      | warn  | A `/v1` call had a missing or wrong API key                  |
| `message request rejected`              | warn  | A send request was malformed or too large                    |
| `message sent`                          | info  | Meta accepted a message. Has `wamid`, type, masked number, duration |
| `message send replayed`                 | info  | A repeated `Idempotency-Key` returned the earlier result without sending |
| `media uploaded`                        | info  | A file was streamed to Meta. Has `media_id`, `mime_type`, `bytes`, duration |
| `media downloaded`                      | info  | A file was streamed to your service. Has `media_id`, `mime_type`, `bytes`, duration |
| `media request rejected`                | warn  | The upload was malformed, for example not multipart or missing a file part |
| `media request failed`                  | warn or error | Meta refused or was unreachable. Has `code`, `meta_code`, and the `trace_id` |
| `media stream interrupted`              | error | A download broke after it had started. The bytes sent so far are in `bytes` |
| `idempotency key could not be recorded, a retry may send the message again` | error | The message was sent but the key could not be saved. Investigate the database |
| `message send failed`                   | warn or error | A send failed. Error level for gateway or Meta side faults (5xx), warn for rejections |

### Example: one message from arrival to delivery

A message arrives while the receiving service is down, then recovers. The logs below are real output with the timestamps removed.

```json
{"level":"INFO","msg":"event received","request_id":"req_2888beff7b9f7111","event_id":"evt_67b8bb76071ee67274b4d7e1f101c868","wamid":"wamid.HBgM1","type":"message.inbound","subtype":"text","phone":"9477*****00","outcome":"queued"}
{"level":"INFO","msg":"request","request_id":"req_2888beff7b9f7111","method":"POST","route":"/webhook","status":200,"duration_ms":0}
{"level":"WARN","msg":"event delivery failed, will retry","event_id":"evt_67b8bb76071ee67274b4d7e1f101c868","topic":"wa.inbound","attempts":1,"retry_in_ms":263,"error":"webhook responded with status 500"}
{"level":"WARN","msg":"event delivery failed, will retry","event_id":"evt_67b8bb76071ee67274b4d7e1f101c868","topic":"wa.inbound","attempts":2,"retry_in_ms":393,"error":"webhook responded with status 500"}
{"level":"INFO","msg":"event delivered","event_id":"evt_67b8bb76071ee67274b4d7e1f101c868","topic":"wa.inbound","attempts":3,"duration_ms":0,"lag_ms":2056}
```

A repeated webhook for the same message produces `outcome: "duplicate"` with an empty `event_id`, because no new event was created.

### Tracing recipes

```sh
# Everything about one message
docker compose logs gateway | grep 'wamid.HBgM1'

# Every delivery attempt for an event
docker compose logs gateway | grep 'evt_67b8bb76071ee67274b4d7e1f101c868'

# All failures
docker compose logs gateway | grep -E '"level":"(WARN|ERROR)"'

# With jq, events that took more than 5 seconds to deliver
docker compose logs --no-log-prefix gateway | jq -c 'select(.msg=="event delivered" and .lag_ms>5000)'
```

## Metrics

Prometheus text format at `GET /metrics`. Go runtime and process metrics are included. Keep this endpoint on an internal network.

| Metric                                         | Type      | Labels                | What it tells you                                         |
| ---------------------------------------------- | --------- | --------------------- | --------------------------------------------------------- |
| `zephyr_webhook_requests_total`                | counter   | `result`              | Webhook outcomes: `accepted`, `ignored`, `invalid_signature`, `malformed`, `too_large`, `persist_error` |
| `zephyr_events_ingested_total`                 | counter   | `type`, `outcome`     | Events parsed from webhooks, `queued` versus `duplicate`  |
| `zephyr_delivery_attempts_total`               | counter   | `topic`, `result`     | Attempts by `success`, `retry`, or `failed`               |
| `zephyr_delivery_duration_seconds`             | histogram | `topic`, `result`     | How long a single delivery attempt takes                  |
| `zephyr_delivery_lag_seconds`                  | histogram | `topic`               | Time from acceptance to delivery, including retries       |
| `zephyr_outbox_entries`                        | gauge     | `state`               | Events waiting (`pending`) and given up (`failed`)        |
| `zephyr_outbox_oldest_pending_age_seconds`     | gauge     | none                  | Age of the oldest waiting event, `0` when empty           |
| `zephyr_outbox_stats_error`                    | gauge     | none                  | `1` if the queue could not be read during the scrape      |
| `zephyr_send_requests_total`                   | counter   | `type`, `result`      | Send requests by message type and result (`sent`, `replayed`, or an error code) |
| `zephyr_send_duration_seconds`                 | histogram | `type`                | Time to handle a send, including the call to Meta         |
| `zephyr_media_requests_total`                  | counter   | `operation`, `result` | Media transfers by `upload` or `download` and result (`ok`, `interrupted`, or an error code) |
| `zephyr_media_duration_seconds`                | histogram | `operation`           | Time to complete a transfer                               |
| `zephyr_media_bytes_total`                     | counter   | `operation`           | Bytes streamed in each direction                          |
| `zephyr_http_requests_total`                   | counter   | `method`, `route`, `status` | Request counts                                      |
| `zephyr_http_request_duration_seconds`         | histogram | `method`, `route`     | Request latency                                           |

`route` is the route pattern, with `unmatched` for unknown paths, so labels stay bounded.

### Useful queries

```text
# Messages accepted per second
sum(rate(zephyr_events_ingested_total{outcome="queued"}[5m]))

# Share of deliveries that failed on the first try
sum(rate(zephyr_delivery_attempts_total{result="retry"}[5m]))
  / sum(rate(zephyr_delivery_attempts_total[5m]))

# 95th percentile time from acceptance to delivery
histogram_quantile(0.95, sum(rate(zephyr_delivery_lag_seconds_bucket[5m])) by (le))

# Rejected webhooks (possible misconfigured secret or probing)
sum(rate(zephyr_webhook_requests_total{result="invalid_signature"}[5m]))

# Share of sends that succeeded
sum(rate(zephyr_send_requests_total{result="sent"}[5m]))
  / sum(rate(zephyr_send_requests_total[5m]))

# Media throughput in bytes per second
sum by (operation) (rate(zephyr_media_bytes_total[5m]))

# Downloads that could not find their media (expired IDs)
sum(rate(zephyr_media_requests_total{operation="download",result="media_not_found"}[5m]))

# Retries absorbed by idempotency keys
sum(rate(zephyr_send_requests_total{result="replayed"}[5m]))

# Sends failing for a given reason
sum by (result) (rate(zephyr_send_requests_total{result!="sent"}[5m]))
```

### Suggested alerts

| Alert                      | Condition                                                         | Meaning                                                  |
| -------------------------- | ----------------------------------------------------------------- | -------------------------------------------------------- |
| Backlog growing            | `zephyr_outbox_oldest_pending_age_seconds > 300` for 5 minutes    | Your service is down or slow                             |
| Events failing permanently | `increase(zephyr_outbox_entries{state="failed"}[1h]) > 0`         | Events exhausted retries and need manual attention       |
| Persistence errors         | `increase(zephyr_webhook_requests_total{result="persist_error"}[5m]) > 0` | Disk or database trouble, Meta is retrying       |
| Signature rejections       | `rate(zephyr_webhook_requests_total{result="invalid_signature"}[5m]) > 0` | Wrong app secret, or someone is probing the endpoint |
| Not scraping               | `zephyr_outbox_stats_error == 1`                                  | The store cannot be read                                 |
| Token problem              | `increase(zephyr_send_requests_total{result="upstream_auth_failed"}[10m]) > 0` | The Meta access token expired or lost permission |
| Broken downloads           | `increase(zephyr_media_requests_total{result="interrupted"}[15m]) > 0` | Media downloads are dropping midway |
| Sends failing              | `sum(rate(zephyr_send_requests_total{result=~"upstream_.*|rate_limited"}[5m])) > 0` | Meta is rejecting, throttling, or unreachable |

### Scrape configuration

```yaml
scrape_configs:
  - job_name: zephyr
    static_configs:
      - targets: ['zephyr:8080']
```

## Request IDs

Every HTTP response includes `X-Request-Id`. Webhook log lines carry the same value, so a single request can be located in the logs from the response header. Zephyr generates the ID itself and ignores any ID sent by the caller.
