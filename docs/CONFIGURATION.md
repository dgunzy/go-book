# Configuration & Book Settings

This is the single reference for every operator-tunable setting. If you add a new
knob, add it here in the same change, wire it through `internal/config`, and — if
it changes what an admin sees or does — surface it on the in-app help page
(`/admin/help`, `web/templates/admin_help.gohtml`).

There are two kinds of settings:

1. **Process settings** — loaded once at startup from environment variables by
   `internal/config` (`config.Load`). Change them in the GitOps ConfigMaps
   (`gitops/apps/cabot-cup-next/kustomization.yaml` for production and
   `gitops/apps/cabot-cup-next/acceptance/kustomization.yaml` for the test book),
   then let Flux roll the change out. They apply book-wide.
2. **Per-player settings** — stored in PostgreSQL and changed by an admin/owner in
   the **Members** admin UI (`/admin/members`). They override the book default for
   one member.

## Book behaviour (the ones you tune most)

| Setting | Env var | Default | Meaning | Where to change |
|---|---|---|---|---|
| Auto-approve max stake | `WAGER_AUTO_APPROVE_MAX_CENTS` | `10000` ($100) | Stakes at or below this are accepted immediately; larger ones wait for manual admin approval. `0` sends every wager to manual review. | GitOps ConfigMap; per-player override on Members page |
| Dynamic-pricing liquidity | `PRICING_LIQUIDITY_DEFAULT_CENTS` | `300000` ($3,000) | Default line-movement sensitivity ("b") for a new market when the admin enables dynamic pricing without typing a value. **Larger = the line moves less per dollar of action.** Set per market on the create form. | GitOps ConfigMap; per market on the create form |
| Credit limit (per player) | — (DB column `users.credit_limit_cents`) | $1,000 | How far a member's balance may go negative before wagers are refused. | Members page only |
| Auto-approve override (per player) | — (DB column `users.wager_auto_approve_max_cents`) | unset → book default | Per-player auto-approve threshold; blank uses the book default, `0` forces manual review. | Members page only |

### Reference: dynamic-pricing liquidity

The engine tilts the backed side's weight by `exp(stake / b)` and renormalises to
preserve the overround, so a bigger `b` means gentler moves. For a `-110 / -110`
match with money on one side:

| Bet | b = $3,000 (default) |
|---|---|
| $100 | ≈ -114 |
| $300 | ≈ -122 |
| $600 | ≈ -136 |
| $1,000 | ≈ -157 |

Lower `b` (e.g. $1,000) for a livelier line; raise it (e.g. $10,000) for a very
sticky one. Every accepted wager keeps the exact price it was shown; a move only
affects the next bettor.

## Platform / runtime settings

| Env var | Default | Meaning |
|---|---|---|
| `APP_ENV` | `development` | `development`, `test`, `staging`, or `production`. |
| `PORT` / `HOST` | `8080` / `0.0.0.0` | Listen address. |
| `PUBLIC_BASE_URL` | `http://localhost:<port>` | Absolute base URL; must be https in staging/production. |
| `PRIVATE_APP_ENABLED` | `false` | Turns on the authenticated betting/bookkeeping area. |
| `SESSION_TTL` | `12h` | Session lifetime (1m–7d). |
| `LOGIN_ATTEMPT_TTL` | `10m` | OIDC login-attempt lifetime (1m–30m). |
| `SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown budget. |

## Database selection

| Env var | Default | Meaning |
|---|---|---|
| `DATABASE_MODE` | `real` | `real` or `test`. `test` points the **entire process** at `TEST_DATABASE_URL`. Never allowed with `APP_ENV=production`. |
| `DATABASE_URL` | — | Real database DSN (required when the private app is enabled). |
| `TEST_DATABASE_URL` | — | Isolated test database DSN; must differ from `DATABASE_URL`. |

## Identity (OIDC)

Required when `PRIVATE_APP_ENABLED=true`: `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`,
`OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL` (path must be `/auth/callback` and share
the `PUBLIC_BASE_URL` host). These are supplied from ExternalSecrets, never Git.

## Where each book setting lives in code

- Defaults and env parsing: `internal/config/config.go` (one `const default…` per
  setting; this is the source of truth for defaults).
- Wired into handlers in `cmd/cabot/main.go` and passed to the web layer via each
  handler's `Dependencies`.
- `internal/bettingpg.DefaultPricingLiquidityCents` is a compiled-in fallback only,
  kept in sync with `config.defaultPricingLiquidityCents`; the configured value is
  threaded down and normally wins.
- Admin-facing explanation: `web/templates/admin_help.gohtml` ("Current settings &
  defaults"), which renders the live values.
