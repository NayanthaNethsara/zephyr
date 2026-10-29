# Zephyr

Open-source, self-hosted gateway for Meta's official WhatsApp Cloud API, written in Go. Zephyr verifies, deduplicates, rate-limits, retries, and routes messages between WhatsApp and your own systems. It contains no bot or business logic.

The architecture and implementation spec lives in [docs/wa-gateway-architecture.md](docs/wa-gateway-architecture.md).

## Requirements

- Go 1.25 or later
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

The gateway listens on `GATEWAY_PORT` (default `8080`).

```sh
curl localhost:8080/healthz
```

Run `make help` for all targets.

## Docker

```sh
make up
make logs
make down
```

`make up-nats` also starts a NATS server with JetStream for broker transport development.

## Configuration

| Variable       | Default | Description                         |
| -------------- | ------- | ----------------------------------- |
| `GATEWAY_PORT` | `8080`  | HTTP port                           |
| `LOG_LEVEL`    | `info`  | `debug`, `info`, `warn`, or `error` |

## License

Apache 2.0. See [LICENSE.txt](LICENSE.txt).
