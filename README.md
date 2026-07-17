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
- Google OIDC sign-in for pre-approved members, server-side hashed sessions, and
  CSRF-protected sign-out.
- Authenticated member balances, immutable ledger activity, current and archived
  wager history, and an owner/admin reconciliation view.
- Repeatable Turso archive reconciliation and promotion into approved accounts,
  balanced opening entries, and immutable historical transaction/wager tables.
- Pure Go ledger, American-odds, event-envelope, and match-verification domain types.
- Structured request logging, request IDs, panic recovery, security headers, health
  endpoints, validated configuration, and graceful shutdown.
- Staged GitOps deployment and isolated `cabot_cup` database resources, kept at zero
  replicas with no production route until secrets and an immutable image are ready.

Result-entry handlers, wager application services, outbox dispatch, and direct S3
upload handlers are the next implementation slices. The removed Turso-era
application remains available in Git history for migration reference.

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
- `/login`
- `/book`, `/book/ledger`, and `/book/wagers` after approved sign-in
- `/admin` for admins and owners
- `/livez` and `/readyz`

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `test`, `staging`, or `production` |
| `HOST` | `0.0.0.0` | Listen hostname or IP without a port |
| `PORT` | `8080` | Listen port |
| `PUBLIC_BASE_URL` | `http://localhost:${PORT}` | HTTPS required in staging/production |
| `PRIVATE_APP_ENABLED` | `false` | Enables PostgreSQL, OIDC, and authenticated routes |
| `DATABASE_URL` | empty locally | Required when the private app is enabled; never log it |
| `OIDC_ISSUER_URL` | empty | OIDC discovery issuer, initially Google |
| `OIDC_CLIENT_ID` | empty | OIDC client identifier |
| `OIDC_CLIENT_SECRET` | empty | OIDC client secret; load through External Secrets |
| `OIDC_REDIRECT_URL` | empty | Absolute callback URL ending in `/auth/callback` |
| `SESSION_TTL` | `12h` | Session lifetime, bounded to seven days |
| `LOGIN_ATTEMPT_TTL` | `10m` | OIDC state/nonce lifetime, bounded to 30 minutes |
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

The legacy private-book import is a separate, explicit operator action. The
database role used for these commands must have read access to the restricted
`legacy_cabot_book` archive; the web role does not need that access.

```sh
DATABASE_URL="$MIGRATION_DATABASE_URL" go run ./cmd/cabot legacy-book-report
LEGACY_BOOK_EXPECTED_SOURCE_VERSION="$REVIEWED_SOURCE_VERSION" \
  DATABASE_URL="$MIGRATION_DATABASE_URL" go run ./cmd/cabot legacy-book-promote
```

The first command emits a redacted deterministic summary and source hash; it does
not write member emails, balances by identity, or wager descriptions to logs.
Promotion requires that reviewed hash explicitly, refuses blocking issues, records
the CAD 282.00 source discrepancy, uses balanced ledger postings, copies immutable
history, and verifies existing rows on every rerun.

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
