# Limitations

This page lists what Zephyr 0.1 does not do and the behaviors to plan around. Items marked "planned" are in the roadmap in [wa-gateway-architecture.md](wa-gateway-architecture.md).

## Not built yet

| Gap                                   | Impact                                                                        | Status                  |
| ------------------------------------- | ----------------------------------------------------------------------------- | ----------------------- |
| No sending of messages                | Zephyr is receive-only. Replies must go to Meta by your own means             | Planned, next release   |
| No API key authentication             | There is no authenticated API yet, so nothing to protect. `/metrics` is open  | Planned with the send API |
| No list or replay API for failed events | Failed events need manual SQL to inspect or retry (see user-flows.md, flow 10) | Planned               |
| Only the `http` transport             | No NATS, Redis, RabbitMQ, or Kafka delivery                                    | Planned                 |
| SQLite only                           | No PostgreSQL support                                                          | Planned                 |
| One WhatsApp number per deployment    | Webhooks for other numbers are forwarded but there is no per-number config     | Planned                 |
| No 24-hour session tracking           | Not needed for receive-only                                                    | Planned with sending    |
| No rate limiting                      | Neither inbound requests nor outbound delivery are throttled                   | Planned                 |
| No media handling                     | Media messages arrive with Meta's media ID only. Zephyr does not download them | Not planned for 0.x     |
| No dashboard                          | Everything is logs and metrics                                                 | Later                   |

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
| `/metrics`, `/healthz`, `/readyz` are unauthenticated | Expose only `/webhook` publicly                                                            |
| Message content is stored temporarily  | Plaintext in the SQLite file while queued, and for `FAILED_RETENTION` if delivery fails. Protect the volume and its backups |
| Secrets live in environment variables  | Use your platform's secret store. There is no secret rotation support                             |
| Inbound replay window                  | Meta does not send a timestamp. A captured, validly signed request could be replayed after `DEDUP_TTL` expires |
| Dependencies are not scanned in CI     | Run `govulncheck` regularly. No CI pipeline is configured yet                                      |
| Lint is not yet enforced               | `.golangci.yml` exists but is only run manually with `make lint`                                   |

## Compliance

Zephyr connects to Meta's official WhatsApp Business Platform. You are responsible for following Meta's Business Messaging Policy and Business Terms, including user opt-in rules. Zephyr does not inspect or restrict what you connect behind it.
