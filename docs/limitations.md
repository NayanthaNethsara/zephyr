# Limitations

This page lists what Zephyr 0.1 does not do and the behaviors to plan around. Items marked "planned" are in the roadmap in [wa-gateway-architecture.md](wa-gateway-architecture.md).

## Not built yet

| Gap                                   | Impact                                                                        | Status                  |
| ------------------------------------- | ----------------------------------------------------------------------------- | ----------------------- |
| Only the `http` and `nats` transports | No Redis, RabbitMQ, or Kafka delivery                                          | Planned                 |
| SQLite only                           | No PostgreSQL support                                                          | Planned                 |
| One WhatsApp number per deployment    | Webhooks for other numbers are forwarded but sending uses the one configured number | Planned            |
| Limited rate limiting                 | Only sends are throttled, per number and per process. Inbound requests are not  | Planned                 |
| No media storage                      | Media streams through Zephyr and is never kept. Your service must store files it wants to keep | By design, optional storage may come later |
| No dashboard                          | Everything is logs and metrics                                                 | Later                   |

## Sending

| Behavior                                    | Detail and workaround                                                                                         |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Sends wait by default                       | The call blocks until Meta answers, up to 15 seconds. Set a client timeout above that, or use `?async=true`    |
| Synchronous sends are not retried           | A failure is returned to you with `retryable`. Your service owns retries. Queued sends (`?async=true` or the broker) are retried by Zephyr |
| A timeout can hide a successful send        | On `upstream_timeout` or a dropped connection the message may have been accepted. Send an `Idempotency-Key` so a retry is safe, and treat the `message.status` events as the source of truth |
| Idempotency is opt-in                       | Without the `Idempotency-Key` header every call sends, and blind retries can duplicate                         |
| Idempotency has two narrow gaps             | A crash between Meta accepting a message and Zephyr recording it, and a database failure while recording. Both can allow one duplicate on retry. The second is logged as an error |
| Only successful sends are remembered        | A failed send releases its key so the retry runs again. A key is kept for `IDEMPOTENCY_TTL` (24 hours by default) |
| Individual recipients only                  | Group messages, replies with `context`, read receipts, and typing indicators are not exposed                   |
| The window check is best effort             | Zephyr refuses only when it knows the window expired. A customer it has no record of (`unknown`) is passed to Meta, which decides. Use a template to reach customers outside the window |
| Only required fields are validated          | Everything else is passed to Meta as is, so new Meta features work, and Meta's rejection comes back as a code  |
| Error codes follow Meta's published table   | Code `131030` (recipient not allowed on a test number) is not in that table but is handled. Unlisted codes fall back to the HTTP status |
| API keys are static                         | They live in `API_KEYS`. Rotating or revoking a key means a restart. There are no scopes                       |
| Sent messages are not stored                | Zephyr logs metadata only (message ID, type, masked number). Delivery results arrive as `message.status` events |

## Queued sends and sessions

| Behavior                                       | Detail and workaround                                                                                         |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| One message at a time                          | The send worker sends queued messages one after another, so a slow Meta response delays the ones behind it     |
| Statuses expire                                | A queued message's status is kept for `IDEMPOTENCY_TTL`. After that `GET /v1/messages/{id}` returns `404`. Store the `message.result` event if you need a longer record |
| Retries stop after `RETRY_MAX_ATTEMPTS`        | The message then fails with the last error and a failed `message.result` is sent. Resend it yourself          |
| Sessions are learned from Zephyr's own inbound | A customer who wrote before Zephyr was installed is `unknown` until they write again                           |
| Changing `PHONE_HASH_KEY` forgets sessions     | Every customer becomes `unknown` until they write again. Nothing is blocked while they are unknown            |
| The rate limit is per process                  | Running two Zephyr processes against different databases doubles the allowed rate. Only one instance is supported anyway |
| Rate limit waits are capped at 2 seconds       | A synchronous call that would wait longer returns `429`. Queued messages are retried with backoff             |

## NATS

| Behavior                                       | Detail and workaround                                                                                         |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| No `wa.dlq` subject                            | A request that cannot succeed produces a failed `message.result` on `wa.results`, and events that cannot be published stay in the failed list (`GET /v1/events/failed`) |
| One subject per topic                          | Events are published to `wa.inbound` and so on without a per-customer subject. Order within a topic is preserved, and there is no partitioning |
| Stream keeps messages for 7 days               | A consumer that is offline longer than that loses events. The stream is created for you with file storage      |
| Delivery is at least once                      | Treat the event `id` as an idempotency key. Publishing is deduplicated only within JetStream's short window    |
| Events are not signed                          | Authentication is whatever your NATS server enforces. Use credentials and TLS (`tls://`)                       |
| Credentials in the URL only                    | `NATS_URL` can carry a user and password. There are no separate settings for token or key files               |
| No build tag                                   | The NATS client is always compiled in, which adds under 1 MB to the binary (about 20.7 MB in total)            |

## Media

| Behavior                                      | Detail and workaround                                                                                         |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Zephyr keeps no copy                          | A download is a live pass-through from Meta. Store files you need in your own bucket when you download them    |
| Inbound media expires after 7 days            | After that `GET /v1/media/{id}` returns `404 media_not_found`. Download shortly after the event arrives        |
| Uploaded media expires after 30 days          | Upload again if you need to send the same file later, or send it by public `link`                              |
| Sending by link depends on Meta caching       | Meta caches a linked file for about 10 minutes, so a changed file at the same URL may not be picked up at once |
| Size limits follow the MIME type              | Images 5 MB, audio and video 16 MB, stickers 500 KB, everything else 100 MB. Over the limit returns `413`, and a static sticker between 100 and 500 KB is left for Meta to refuse |
| MIME type is not checked against a list       | Meta decides what is supported, so new formats work without a release, but a wrong type is learned from Meta   |
| One upload per request                        | Only the first `file` part is used. Send several files with several requests                                   |
| An interrupted download cannot change status  | The `200` is already sent. Check the `Content-Length` and the `X-Media-Sha256` header to detect a short file   |
| Transfers are limited to 5 minutes            | A very slow connection to a 100 MB file may not finish                                                         |

## Delivery behavior

| Behavior                              | Detail and workaround                                                                                  |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| At-least-once delivery                | A crash after your service responds but before Zephyr records it causes a repeat. Deduplicate on the event `id` |
| No ordering guarantee                 | A failing event is retried later, so newer events can arrive first. Use the event `occurred_at` and the message `timestamp` to order |
| Delivery is sequential                | One event at a time. A slow endpoint (up to 10 s per attempt) delays everything behind it               |
| A backlog is unbounded                | A long outage grows the SQLite file. Watch `zephyr_outbox_entries` and disk space                       |
| Exhausted events stop retrying        | After `RETRY_MAX_ATTEMPTS` (default 8, about 2 to 4 minutes) the event is marked failed. Raise the setting if your service has longer outages |
| Failed events expire                  | They are deleted after `FAILED_RETENTION` (7 days by default)                                           |
| Redirects count as failures           | Point `WEBHOOK_URL` at the final URL                                                                    |

## Inbound behavior

| Behavior                                      | Detail                                                                                                     |
| --------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| A malformed item rejects the whole webhook    | If one message in a webhook batch lacks an ID, the request gets `400` and none of it is saved. Meta may keep retrying that request. Watch `zephyr_webhook_requests_total{result="malformed"}` |
| Sender profile name is not forwarded          | Meta puts the profile name in a `contacts` block next to the message. Zephyr forwards only the message itself under `raw` |
| Dedup window is finite                        | Messages are remembered for `DEDUP_TTL` (48 hours). A Meta redelivery after that window is treated as new   |
| Only the `messages` webhook field is handled  | Other fields (account updates, template status, and so on) are ignored with `200`                           |
| Message types are passed through              | Zephyr does not validate or interpret message bodies. Unknown types arrive under `raw`                      |

## Deployment

| Limitation                       | Detail                                                                                               |
| -------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Single instance only             | SQLite and the in-process worker mean one running copy. Two instances on one file would both deliver every event |
| No horizontal scaling            | Scale the receiving side, not Zephyr. A single instance handles typical business messaging volumes   |
| No built-in TLS                  | Meta requires HTTPS. Run a reverse proxy such as Caddy or nginx in front                              |
| State lives on one disk          | Back up the data volume. If it is lost, queued and failed events and dedup history are lost            |
| Data volume must persist         | In Docker, remove the `gateway_data` volume and you lose the queue                                    |
| Config changes need a restart    | There is no hot reload                                                                                |

## Security notes

| Item                                   | Detail                                                                                            |
| -------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `/metrics`, `/healthz`, `/readyz` are unauthenticated | Expose only `/webhook` and, if you want remote sending, `/v1/messages` publicly            |
| `WA_ACCESS_TOKEN` grants sending rights | It lives in the environment and is sent only to Meta over HTTPS. Treat the `.env` file as a secret and use a system user token with the minimum permissions |
| Message content is stored temporarily  | Plaintext in the SQLite file while queued, and for `FAILED_RETENTION` if delivery fails. Protect the volume and its backups |
| Secrets live in environment variables  | Use your platform's secret store. There is no secret rotation support                             |
| Inbound replay window                  | Meta does not send a timestamp. A captured, validly signed request could be replayed after `DEDUP_TTL` expires |
| Dependencies are not scanned in CI     | Run `govulncheck` regularly. No CI pipeline is configured yet                                      |
| Lint is not yet enforced               | `.golangci.yml` exists but is only run manually with `make lint`                                   |

## Compliance

Zephyr connects to Meta's official WhatsApp Business Platform. You are responsible for following Meta's Business Messaging Policy and Business Terms, including user opt-in rules. Zephyr does not inspect or restrict what you connect behind it.
