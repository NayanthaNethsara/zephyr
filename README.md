# Zephyr

Open-source, self-hosted gateway for Meta's official WhatsApp Cloud API, written in Go. Zephyr verifies, deduplicates, rate-limits, retries, and routes messages between WhatsApp and your own systems. It contains no bot or business logic.

The architecture and implementation spec lives in [docs/wa-gateway-architecture.md](docs/wa-gateway-architecture.md).

## Requirements

- Go 1.26 or later
- Docker with Compose (optional)

## Structure

```text
apps/
  gateway/   Go service (Gin API, transports, storage)
docs/        Architecture and setup docs
```

## Development

```sh
make setup
make run
```

Fill in `.env` before running. The gateway refuses to start when required values are missing.

The gateway listens on `GATEWAY_PORT` (default `8080`).

```sh
curl localhost:8080/healthz
```

## Connect WhatsApp

1. In the Meta app dashboard, set the webhook callback URL to `https://<your-host>/webhook` and the verify token to `META_VERIFY_TOKEN`.
2. Set `META_APP_SECRET` to the app secret so Zephyr can verify `X-Hub-Signature-256`.
3. Subscribe the app to the `messages` webhook field.
4. Set `WEBHOOK_URL` to your service. Zephyr forwards each inbound message and status update there as a signed POST. See [docs/events.md](docs/events.md).

Run `make help` for all targets.

## Docker

```sh
make up
make logs
make down
```

`make up-nats` also starts a NATS server with JetStream for broker transport development.

## Configuration

| Variable                 | Default                     | Description                                        |
| ------------------------ | --------------------------- | -------------------------------------------------- |
| `GATEWAY_PORT`           | `8080`                      | HTTP port                                          |
| `LOG_LEVEL`              | `info`                      | `debug`, `info`, `warn`, or `error`                |
| `DB_URL`                 | `sqlite:///data/gateway.db` | SQLite database location                           |
| `META_APP_SECRET`        | required                    | Verifies Meta webhook signatures                   |
| `META_VERIFY_TOKEN`      | required                    | Answers Meta's webhook verification handshake      |
| `TRANSPORT`              | `http`                      | Delivery transport, only `http` is available       |
| `TRANSPORT_TOPIC_PREFIX` | `wa`                        | Topic prefix                                       |
| `WEBHOOK_URL`            | required                    | Your service endpoint that receives events         |
| `WEBHOOK_SIGNING_SECRET` | required                    | Signs the webhooks Zephyr sends to `WEBHOOK_URL`   |
| `RETRY_MAX_ATTEMPTS`     | `8`                         | Delivery attempts before an event is marked failed |
| `RETRY_BASE_DELAY`       | `2s`                        | Exponential backoff base, with jitter              |
| `DEDUP_TTL`              | `48h`                       | How long seen message IDs are remembered           |
| `FAILED_RETENTION`       | `168h`                      | How long failed events are kept                    |

## Security notes

- Zephyr serves plain HTTP. Meta requires HTTPS, so run it behind a TLS-terminating reverse proxy and only expose `/webhook` publicly.
- `META_VERIFY_TOKEN` and `WEBHOOK_SIGNING_SECRET` must be at least 16 random characters. Generate them with `openssl rand -hex 32`.
- Verify the signature and reject old timestamps on the receiving side. See [docs/events.md](docs/events.md).
- Message content is stored in the SQLite file only while an event is queued, and for `FAILED_RETENTION` if delivery fails. The file is created with mode `0600`. Keep the data volume private.
- Keep tokens out of `WEBHOOK_URL` query strings where possible. Delivery errors are logged without the URL.

## License

Apache 2.0. See [LICENSE](LICENSE).
