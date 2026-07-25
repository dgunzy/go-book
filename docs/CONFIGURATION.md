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
| Auto-approve max stake | `WAGER_AUTO_APPROVE_MAX_CENTS` | `20000` ($200) | Stakes at or below this are accepted immediately; larger ones wait for manual admin approval. `0` sends every wager to manual review. Each member sees the limit that applies to them on their book overview. | GitOps ConfigMap; per-player override on Members page |
| Dynamic-pricing liquidity | `PRICING_LIQUIDITY_DEFAULT_CENTS` | `500000` ($5,000) | Default line-movement sensitivity ("b") for a new market when the admin enables dynamic pricing without typing a value. **Larger = the line moves less per dollar of action.** Sets the pace of movement, not its limit: how far a line can ever travel is bounded by the drift floor in `internal/pricing`, which keeps the book un-arbitrageable. Set per market on the create form. | GitOps ConfigMap; per market on the create form |
| Default credit limit (new members) | `DEFAULT_CREDIT_LIMIT_CENTS` | `150000` ($1,500) | Credit limit a newly invited member is created with: how far their balance may go negative before wagers are refused. Changing it does not move existing members. | GitOps ConfigMap; per-player on Members page |
| Max payout per wager | `WAGER_MAX_PAYOUT_CENTS` | `500000` ($5,000) | Hard ceiling on what one wager may **win**. A longshot price turns a small stake into a large liability, so this bounds what the book can owe on a single bet. Enforced in the store with a compiled default, so no code path can place a wager that escapes it. | GitOps ConfigMap |
| Max stake per member, per market | — (DB column `markets.max_stake_cents`) | none | Caps what one member may have riding on a market at once, counting every pending and accepted wager they hold on it. For fun lines. | Set on the market create form |
| Credit limit (per player) | — (DB column `users.credit_limit_cents`) | the configured default above | A member's own limit once set, overriding the default for them. | Members page only |
| Auto-approve override (per player) | — (DB column `users.wager_auto_approve_max_cents`) | unset → book default | Per-player auto-approve threshold; blank uses the book default, `0` forces manual review. | Members page only |

### Reference: dynamic-pricing liquidity

The engine tilts the backed side's weight by `exp(stake / b)` and renormalises to
preserve the overround, so a bigger `b` means gentler moves. For a `-110 / -110`
match with money on one side:

| Bet | b = $5,000 (default), `-110 / -110` |
|---|---|
| $100 | ≈ -112 / -108 |
| $300 | ≈ -117 / -103 |
| $600 | ≈ -119 / -102 (floor reached) |
| $1,000+ | ≈ -119 / -102 (floor reached) |

Lower `b` for a livelier line; raise it for a stickier one. Every accepted wager
keeps the exact price it was shown; a move only affects the next bettor.

### The drift floor: why the line stops moving

Liquidity sets the pace of movement; it does not set the limit. That is the
**drift floor** in `internal/pricing`, and it is not configurable, because it is
what keeps the book from being arbitraged against its own line movement.

Prices taken at different times can be combined. If a member backs one side at
the opening price and the line then runs far enough, the other side becomes
generous enough that backing it too pays more than the pair costs — a guaranteed
profit whatever the result. The engine therefore lets each selection's implied
probability fall no further than a fixed proportion of itself — 80% of
`margin / overround`, keeping the rest as a cushion against rounding. The sum of
the most generous prices the book will ever post then always stays above even
money. Sharing the budget proportionally rather than equally matters on a long
outright: an equal share of probability is a couple of points on the favourite
but a doubling of the outsider's price.

The practical consequence: **a thin opening line can only move a little.** A
`-110 / -110` market carries a 4.76% margin, so it can travel about two points
per side and no further, however much action lands. Wider openings buy more
room — `-130 / +100` carries 6.52% and moves about three points per side. If you
want livelier movement, post a wider line; there is no setting that grants both
big moves and no arbitrage.

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
| `DATABASE_CONNECT_TIMEOUT` | `30s` | How long startup retries an unreachable database before giving up (1s–45s). See below. |

### Reference: startup database connect

A new pod usually reaches the database a moment before the network policy admits
it, so the very first connect is refused for reasons unrelated to database
health. The process retries with capped exponential backoff (250ms up to 3s)
until this budget runs out, instead of exiting and turning every rollout into a
crash-and-restart. A genuinely unreachable database still fails loudly at the
end of the budget, and a shutdown signal abandons the wait immediately.

**The budget must stay below the deployment's liveness kill deadline**, or the
kubelet kills the process mid-retry — worse than failing fast. With the probes in
`gitops/apps/cabot-cup-next/deployment.yaml` liveness would kill at roughly 50s
(first check 10s, then every 20s, 3 failures), hence the 45s upper bound. The
deployment also sets a `startupProbe` (20 x 2s = 40s of grace) which holds
liveness off until the process is serving, so the two must be changed together.

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
