# Events

Zephyr forwards every inbound WhatsApp message and delivery status to `WEBHOOK_URL` as a signed `POST` with a JSON body. Delivery is at least once: failed deliveries are retried with exponential backoff, so receivers must treat the event `id` as an idempotency key.

Respond with any `2xx` status to acknowledge an event. Anything else, including redirects and timeouts, counts as a failure and is retried up to `RETRY_MAX_ATTEMPTS` times.

## Envelope

```json
{
  "id": "evt_0b4b3a5a4da58182f0cf6274dfa8c527",
  "type": "message.inbound",
  "occurred_at": "2026-10-29T08:44:50Z",
  "phone_number_id": "123456789",
  "waba_id": "987654321",
  "data": {}
}
```

| `type`            | `data`                                                              |
| ----------------- | ------------------------------------------------------------------- |
| `message.inbound` | `wamid`, `from`, `timestamp`, `type`, and the original Meta message under `raw` |
| `message.status`  | `wamid`, `status`, `recipient`, `timestamp`, and `errors` when present |

## Headers

| Header                | Value                                  |
| --------------------- | -------------------------------------- |
| `X-Gateway-Event`     | Event type                             |
| `X-Gateway-Delivery`  | Event id                               |
| `X-Gateway-Timestamp` | Unix seconds when the attempt was sent |
| `X-Gateway-Signature` | `sha256=` followed by the hex digest   |

## Verifying a signature

The digest is `HMAC-SHA256(WEBHOOK_SIGNING_SECRET, timestamp + "." + raw_body)`. Compute it over the raw request bytes, compare in constant time, and reject timestamps older than a few minutes.

```python
import hashlib, hmac

def verify(secret: bytes, timestamp: str, body: bytes, header: str) -> bool:
    expected = "sha256=" + hmac.new(secret, timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```
