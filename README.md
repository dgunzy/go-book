# Cabot Cup

The unified Cabot Cup application combines the public tournament archive and player
statistics with the foundation for invitation-only match reporting and real-money
wager bookkeeping.

The application is a Go modular monolith with server-rendered templates, PostgreSQL,
a transactional outbox, and a double-entry integer-cent ledger. See
[`docs/UNIFICATION_PLAN.md`](docs/UNIFICATION_PLAN.md) for the delivery plan.

## Implemented foundation

- Public home, history, 2019-2024 event, player, and statistics pages.
- Embedded templates, CSS, portraits, and typed legacy snapshot data.
- Validated PostgreSQL schema for identity, competition, result verification, media,
  markets, wagers, ledger, audit, migration provenance, and outbox processing.
- Pure Go ledger, American-odds, event-envelope, and match-verification domain types.
- Structured request logging, request IDs, panic recovery, security headers, health
  endpoints, validated configuration, and graceful shutdown.
- Staged GitOps deployment and isolated `cabot_cup` database resources, kept at zero
  replicas with no production route until secrets and an immutable image are ready.

Authentication, result-entry handlers, PostgreSQL repositories, S3 upload handlers,
and wager application services are the next implementation slices. The removed
Turso-era application remains available in Git history for migration reference.

## Run locally

```sh
make verify
APP_ENV=development PORT=8080 go run ./cmd/cabot
```

Open `http://localhost:8080`. Public routes are:

- `/`
- `/history` and `/history/{year}`
- `/players`
- `/stats`
- `/livez` and `/readyz`

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `test`, `staging`, or `production` |
| `HOST` | `0.0.0.0` | Listen hostname or IP without a port |
| `PORT` | `8080` | Listen port |
| `PUBLIC_BASE_URL` | `http://localhost:${PORT}` | HTTPS required in staging/production |
| `DATABASE_URL` | empty locally | Required in staging/production; never log it |
| `SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown deadline |

## Database

The application image embeds its ordered migrations and public legacy seed. Apply
them through the same command used by the versioned Kubernetes migration Job:

```sh
DATABASE_URL="$DATABASE_URL" go run ./cmd/cabot migrate
```

The command takes a PostgreSQL advisory lock, verifies immutable migration checksums,
and is safe to retry. It imports reliable public legacy records idempotently while
leaving aggregate-only history labeled as a snapshot. Do not run the down migration
against an environment containing data.

## Repository checks

```sh
make fmt-check
make test
make vet
make build
```

CI also runs the race detector, applies and rolls back the migration on PostgreSQL 18,
builds the application, and publishes `ghcr.io/dgunzy/cabot-cup` with immutable
timestamped SHA tags on pushes to `main`.
