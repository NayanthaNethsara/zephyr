# Technical reference

## Stack

| Layer          | Choice                                   | Notes                                                    |
| -------------- | ---------------------------------------- | -------------------------------------------------------- |
| Language       | Go 1.26                                  | Version is set by a dependency requirement in `go.mod`   |
| HTTP framework | Gin                                      | Release mode, custom logging middleware                  |
| Storage        | SQLite via `modernc.org/sqlite`          | Pure Go, no CGO, WAL mode                                |
| Migrations     | `pressly/goose` with embedded SQL        | Applied at startup                                       |
| Configuration  | `koanf`                                  | Environment variables over defaults                      |
| Logging        | `log/slog`, JSON to stdout               | Structured, no message content                           |
| Metrics        | `prometheus/client_golang`               | Exposed at `/metrics`                                    |
| Container      | Distroless static, non-root              | About 31 MB image                                        |
| Build          | `make`, Docker BuildKit                  | `-trimpath -buildvcs=false -ldflags="-s -w"`, `CGO_ENABLED=0` |

## Repository layout

```text
apps/gateway/
  cmd/gateway/        Entry point
  internal/           Application packages (see architecture.md)
  migrations/         Embedded SQL migrations
  Dockerfile          Multi-stage build
  .golangci.yml       Lint configuration
docs/                 Documentation
docker-compose.yml    Gateway container and data volume
Makefile              Developer commands
.env.example          Configuration template
```

## Configuration

Values come from environment variables. A `.env` file is loaded by `make` and by Docker Compose. Empty variables are treated as unset, so a blank line in `.env` does not override a default. Invalid values stop startup with an error naming the variable.

| Variable                 | Default                     | Rules                                                     |
| ------------------------ | --------------------------- | --------------------------------------------------------- |
| `GATEWAY_PORT`           | `8080`                      | 1 to 65535                                                |
| `LOG_LEVEL`              | `info`                      | `debug`, `info`, `warn`, `error`                          |
| `DB_URL`                 | `sqlite:///data/gateway.db` | Must start with `sqlite://`. Parent directories are created |
| `META_APP_SECRET`        | required                    | From Meta app settings                                    |
| `META_VERIFY_TOKEN`      | required                    | At least 16 characters                                    |
| `TRANSPORT`              | `http`                      | Only `http` is supported                                  |
| `TRANSPORT_TOPIC_PREFIX` | `wa`                        | Not empty                                                 |
| `WEBHOOK_URL`            | required                    | `http` or `https` URL                                     |
| `WEBHOOK_SIGNING_SECRET` | required                    | At least 16 characters                                    |
| `RETRY_MAX_ATTEMPTS`     | `8`                         | At least 1. Total attempts, including the first           |
| `RETRY_BASE_DELAY`       | `2s`                        | Greater than zero. Go duration syntax                     |
| `DEDUP_TTL`              | `48h`                       | Greater than zero                                         |
| `FAILED_RETENTION`       | `168h`                      | Greater than zero. Go durations have no `d` unit          |

`DB_URL` examples: `sqlite:///data/gateway.db` is the absolute path `/data/gateway.db`, and `sqlite://./data/gateway.db` is relative to the working directory.

## HTTP endpoints

| Method | Path       | Auth                          | Purpose                           | Responses                                     |
| ------ | ---------- | ----------------------------- | --------------------------------- | --------------------------------------------- |
| GET    | `/webhook` | verify token query parameter  | Meta verification handshake       | `200` challenge, `403`                        |
| POST   | `/webhook` | `X-Hub-Signature-256` header  | Receive Meta events               | `200`, `400`, `401`, `413`, `500`             |
| GET    | `/healthz` | none                          | Process is running                | `200 {"status":"ok"}`                         |
| GET    | `/readyz`  | none                          | Database is reachable             | `200 {"status":"ready"}`, `503`               |
| GET    | `/metrics` | none                          | Prometheus metrics                | `200`                                         |

Every response carries an `X-Request-Id` header. Do not expose `/metrics`, `/healthz`, or `/readyz` publicly. Route only `/webhook` through your reverse proxy.

## Outgoing requests to your service

Each event is a `POST` with `Content-Type: application/json` and these headers.

| Header                | Value                                      |
| --------------------- | ------------------------------------------ |
| `X-Gateway-Event`     | `message.inbound` or `message.status`      |
| `X-Gateway-Delivery`  | Event ID, for example `evt_0b4b...`        |
| `X-Gateway-Timestamp` | Unix seconds when this attempt was sent    |
| `X-Gateway-Signature` | `sha256=` and the hex HMAC                 |

The signature is `HMAC-SHA256(WEBHOOK_SIGNING_SECRET, timestamp + "." + raw_body)`. The timestamp changes on every attempt, the event ID does not. Redirects are not followed and count as failures. Any non-`2xx` status is a failure. Details and verification code are in [events.md](events.md).

## Event topics

Topics are the outbox routing names and are prepared for future broker transports. With the `http` transport they are not visible to your service.

| Topic (default prefix) | Event type        |
| ---------------------- | ----------------- |
| `wa.inbound`           | `message.inbound` |
| `wa.status`            | `message.status`  |

## Database

Schema after all migrations (`apps/gateway/migrations/`). Migration `00001` created the tables as `dedup` and `outbox`, and `00002` renamed them to plural names without touching their rows:

```sql
CREATE TABLE dedup_keys (
    dedup_key  TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE TABLE outbox_entries (
    id              TEXT PRIMARY KEY,
    topic           TEXT NOT NULL,
    msg_key         TEXT NOT NULL,
    payload         BLOB NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    state           TEXT NOT NULL DEFAULT 'pending',
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
```

Timestamps are Unix milliseconds. `state` is `pending` or `failed`. Connection settings: WAL journal, `busy_timeout` 5000 ms, `synchronous=NORMAL`, one open connection. The database file is created with mode `0600`.

Migrations are applied automatically at startup. To add one, create the next numbered file in `apps/gateway/migrations/` using the `NNNNN_description.sql` format with `-- +goose Up` and `-- +goose Down` sections.

## Timings and limits

| Item                          | Value                                           |
| ----------------------------- | ----------------------------------------------- |
| Webhook body limit            | 2 MiB                                           |
| Worker poll interval          | 500 ms                                          |
| Worker batch size             | 50 entries                                      |
| Delivery timeout per attempt  | 10 s                                            |
| Retry delay                   | `RETRY_BASE_DELAY` doubled per attempt, max 10 min, jitter between 50% and 100% |
| Janitor interval              | 1 min                                           |
| Server read header timeout    | 10 s                                            |
| Server read and write timeout | 30 s each                                       |
| Server idle timeout           | 2 min                                           |
| Graceful shutdown timeout     | 30 s                                            |
| Readiness check timeout       | follows the request                             |
| Metrics store read timeout    | 2 s                                             |

## Building and testing

```sh
make test          # go test -race ./...
make build         # static binary at apps/gateway/bin/zephyr
make lint          # golangci-lint (install it first)
make fmt vet tidy
```

Tests use fakes for the store and transport, an in-process HTTP server for delivery, and temporary SQLite files for the store. Table-driven tests cover signature verification, payload parsing, config validation, backoff bounds, and the delivery state machine.

## Docker

```sh
make up        # build and start the gateway
make down
```

| Setting             | Value                                                                    |
| ------------------- | ------------------------------------------------------------------------ |
| Base image          | `gcr.io/distroless/static-debian12:nonroot`                              |
| User                | `nonroot`                                                                |
| Root filesystem     | Read-only                                                                |
| Writable path       | `/data`, a named volume `gateway_data`                                   |
| `DB_URL` in Compose | `sqlite:///data/gateway.db`                                              |
| Exposed port        | `GATEWAY_PORT` on the host maps to `8080` in the container               |
| Extra hardening     | `no-new-privileges`                                                      |

There is no container healthcheck because the image has no shell. Use `/healthz` or `/readyz` from your orchestrator.

## Identifiers

| ID           | Format                         | Created by | Purpose                                      |
| ------------ | ------------------------------ | ---------- | -------------------------------------------- |
| Event ID     | `evt_` plus 32 hex characters  | core       | Idempotency key for receivers, trace key     |
| Request ID   | `req_` plus 16 hex characters  | api        | Ties a webhook request to its log lines      |
| `wamid`      | Meta's message ID              | Meta       | Dedup key and message reference              |
