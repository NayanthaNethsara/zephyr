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
| `message.result`  | `id`, `status`, `wamid` or `error`, and `correlation_id`. The outcome of a queued send, see [Send results](#send-results) |

## Send results

When you send with `?async=true`, or by publishing to the broker, the result arrives as a `message.result` event once Zephyr has finished with the message. Synchronous sends do not produce one, because the caller already has the response.

```json
{
  "id": "evt_5b2f0c1a9d2e4c7e8f3a1b6c7d8e9f01",
  "type": "message.result",
  "occurred_at": "2026-11-02T10:00:01Z",
  "phone_number_id": "123456789",
  "waba_id": "",
  "data": {
    "id": "msg_0b4b3a5a4da58182f0cf6274dfa8c527",
    "status": "sent",
    "wamid": "wamid.HBgL...",
    "correlation_id": "order-1234"
  }
}
```

A failed result carries an `error` object instead of `wamid`:

```json
{ "id": "msg_...", "status": "failed", "correlation_id": "order-1234",
  "error": { "code": "window_closed", "message": "...", "retryable": false } }
```

`data.id` is the `id` returned when the message was queued, and `correlation_id` is the value you supplied. The message content is never included. The event is delivered with the same retries and signature as any other event.

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

## Media in inbound events

For `image`, `video`, `audio`, `document`, and `sticker` messages, the media is not inside the event. The original Meta message under `data.raw` holds a media object named after the type, with the media `id`, `mime_type`, and `sha256`. Images, videos, and documents may also carry `caption`, and documents carry `filename`. Meta is rolling out a `url` field as well, which you can ignore, because `GET /v1/media/{id}` is the supported way to download.

```json
{
  "type": "message.inbound",
  "data": {
    "type": "image",
    "raw": { "type": "image", "image": { "id": "1234567890", "mime_type": "image/jpeg", "sha256": "..." } }
  }
}
```

Download the file with `GET /v1/media/1234567890` and your API key, as described in [api.md](api.md). Do it soon, since Meta keeps inbound media for 7 days.

## NATS topics

With `TRANSPORT=nats` the same envelopes are published to JetStream subjects instead of being posted to `WEBHOOK_URL`. The prefix is `TRANSPORT_TOPIC_PREFIX` (default `wa`). Zephyr creates a stream named after the prefix in upper case (`WA`) that holds every subject under `wa.>` for 7 days.

| Subject       | Direction       | Payload                                           |
| ------------- | --------------- | ------------------------------------------------- |
| `wa.inbound`  | Zephyr to you   | `message.inbound` events                          |
| `wa.status`   | Zephyr to you   | `message.status` events                           |
| `wa.results`  | Zephyr to you   | `message.result` events for queued sends          |
| `wa.outbound` | You to Zephyr   | Send requests, see [api.md](api.md#broker-mode-nats) |

Events are published with the event `id` as the JetStream message ID, so a re-publish after a crash is dropped as a duplicate within JetStream's window, and each message carries a `Zephyr-Key` header with the customer's phone number. Delivery is still at least once, so treat the event `id` as an idempotency key. Messages are not signed, because access is controlled by your NATS credentials. Create a durable consumer on each subject you read, and acknowledge each message after you have handled it.
