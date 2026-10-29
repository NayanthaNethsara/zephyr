# Agent Rules

General rules and technical conventions for working in the Labyrithm codebase.

## Code Style

- Keep proper folder structure.
- Code should be self-explanatory (clear descriptive names, small focused functions).
- Do not comment. Only comment when strictly necessary to explain non-obvious business intent or hardware constraints.
- No unused abstractions, no speculative or future-proofing code. Build only what is asked.
- Use semantic color tokens (`bg-success`, `text-destructive`, etc.), not raw colors.
- Keep one color language per visual signal. Do not reuse the same palette for two different meanings in the same view.
- Strictly no emojis anywhere in responses, comments, or code.

## Architecture

Layer-based, with a strict inward dependency rule. Domain-based packages are not used in this codebase.

```text
cmd/gateway          Wiring and process lifecycle only
internal/api         Delivery layer: HTTP handlers, middleware, request validation
internal/transport   Infrastructure: Transport interface and broker adapters
internal/store       Infrastructure: Storage interface and database implementations
internal/meta        Infrastructure: Meta Graph API client and webhook verification
internal/outbox      Infrastructure: durable outbox and retry worker
internal/core        Business rules: dedup, session window, idempotency, rate limit, routing
internal/config      Configuration loading and validation
internal/crypto      Encryption, hashing, and signing primitives
internal/janitor     TTL cleanup jobs
```

Dependency rules:

- Dependencies point inward: `cmd` -> `api`, adapters -> `core`. Never the reverse.
- `internal/core` imports no adapter package. It may import the interface package of a layer (`store`, `transport`) but never its implementation subpackages (`store/sqlite`, `transport/nats`).
- Adapters never import each other. `api` never imports `store` or `transport` implementations directly; it calls `core`.
- Only `cmd/gateway` constructs concrete implementations and injects them. No global state, no `init()` side effects.
- An interface lives in the root of its layer package (`store/store.go`, `transport/transport.go`); implementations live in subpackages named after the technology.
- Keep interfaces small. Add a method only when a caller needs it.
- Each package owns one responsibility. Do not create `utils`, `common`, `helpers`, `shared`, or `misc` packages.
- Apps are independent. Nothing in `apps/gateway` imports from another app.

Layout rules:

- Go service code lives in `apps/gateway`. Future apps (for example `apps/dashboard`) get their own folder under `apps/`.
- Cross-cutting files stay at the repository root: `Makefile`, `docker-compose.yml`, `.env.example`, `docs/`.
- Add a package or folder only when it contains code. No placeholder folders.

## Naming Conventions

Folders and packages:

- Lowercase, single word, no underscores, hyphens, or plurals: `outbox`, `transport`, `store`.
- Named for what the package provides, not what it contains. Avoid stuttering: `store.Store`, not `store.StoreService`.
- Implementation subpackages are named after the technology: `sqlite`, `postgres`, `nats`, `redis`.
- Command folders under `cmd/` are named after the binary.
- Non-Go folders (`docs`, `migrations`, `api`) are lowercase single words.

Files:

- Go files: lowercase `snake_case.go`, named after the responsibility or main type (`webhook.go`, `retry_worker.go`), never `util.go` or `helpers.go`.
- Tests: `<file>_test.go` next to the file under test.
- Platform- or tag-specific files follow Go conventions (`_linux.go`, build tags).
- Migrations: `NNNNN_short_description.sql` with zero-padded sequence, snake_case description.
- Documentation: `kebab-case.md`.
- Config and tooling files use their ecosystem's standard name (`Makefile`, `Dockerfile`, `docker-compose.yml`, `.golangci.yml`).

Go identifiers:

- Exported: `PascalCase`. Unexported: `camelCase`. Constants follow the same rule, never `SCREAMING_SNAKE_CASE`.
- Acronyms keep one case: `ID`, `URL`, `HTTP`, `API`, `HMAC`, `JSON` (`phoneNumberID`, `NewHTTPClient`).
- Interfaces describe behavior: `Transport`, `Store`, `Handler`. Single-method interfaces use the `-er` form (`Publisher`). No `I` prefix.
- Constructors: `NewXxx`. Functions returning errors name them last and wrap with `%w`.
- Sentinel errors: `ErrXxx`. Custom error types: `XxxError`.
- Receivers: one or two letters, the same on every method of a type.
- `context.Context` is always the first parameter, named `ctx`.
- Booleans read as questions: `isRetryable`, `hasOpenSession`.
- Test functions: `TestXxx`, table-driven with a `name` field and `t.Run`.
- No getters prefixed with `Get`. Use the field name: `Key()`, not `GetKey()`.

Runtime and external names:

- Environment variables: `UPPER_SNAKE_CASE`.
- JSON fields and event types: `snake_case` fields, dot-separated lowercase event types (`message.inbound`).
- REST paths: lowercase, plural nouns, versioned: `/v1/messages`. Health endpoints use `/healthz` and `/readyz`.
- Broker topics: lowercase, dot-separated, with the configurable prefix: `wa.inbound`.
- Database tables and columns: `snake_case`, plural table names (`phone_numbers`), `_at` suffix for timestamps, `_id` for foreign keys.
- Make targets: lowercase, hyphen-separated (`docker-build`, `up-nats`).
- Docker Compose services: lowercase, single word.
- Metric names: `zephyr_` prefix, `snake_case`, unit suffix (`_seconds`, `_total`).

## Git Conventions

- Do not commit or push. The user reviews and commits changes themselves.
- Do not add AI as a co-author in commit messages.
- Commit messages should be short, descriptive, and to the point, in the form `type: description` with `feat`, `fix`, `chore`, `docs`, `refactor`, or `test`.
