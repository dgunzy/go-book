# Cabot Cup Unification Plan

Status: implementation in progress; foundation milestone completed 2026-07-16

Canonical application repository: `go-book`

Canonical domain: `cabotcup.ca`

## Implementation Progress

The first foundation milestone is implemented in `go-book` and staged in `gitops`:

- The canonical Go service now has configuration validation, graceful shutdown,
  health endpoints, structured request logging, security middleware, a production
  container, and CI checks on Go 1.26.5.
- The public history, player, and statistics views are served from typed immutable
  legacy snapshots with local static assets and responsive server-rendered templates.
- PostgreSQL migrations establish identity, competition, media, betting, immutable
  double-entry ledger, audit, migration provenance, and transactional outbox tables.
- Pure competition, ledger, and event contracts cover result confirmation, disputes,
  corrections, balanced postings, reversals, and idempotency with race-tested units.
- GitOps resources stage the CNPG database/role and a zero-replica application without
  changing production traffic. They require the documented AWS secrets before merge.

The event-driven and betting foundations landed 2026-07-17:

- The transactional outbox dispatcher is implemented (`internal/events` +
  `internal/eventspg`): `FOR UPDATE SKIP LOCKED` claiming, bounded exponential
  retries with jitter, terminal-failure retention for operators, stale-lock release,
  and per-consumer `event_receipts` deduplication, verified by unit tests and
  PostgreSQL integration tests. Correctness does not depend on LISTEN/NOTIFY.
- The pure betting domain (`internal/betting`) covers market/wager state machines,
  odds/terms snapshots at placement, acceptance escrow transactions, and a
  deterministic settlement engine (win/loss/push/void plus market voids) that emits
  balanced ledger transactions with reproducible idempotency keys and versioned
  events; a corrected re-settlement uses the settlement version as the event
  aggregate version so it is never dropped by outbox uniqueness.
- Legacy Cabot Book promotion now posts a per-user 2025 season settlement
  transaction (the exact inverse of the opening balance, with an explanatory
  reason) so every imported balance provably returns to zero and the 2026 season
  starts at 0 with fully auditable history.
- `DATABASE_MODE=real|test` flips the server and every CLI command between
  `DATABASE_URL` and `TEST_DATABASE_URL` for end-to-end rehearsal; test mode is
  rejected in production and the two URLs must differ. GitOps stages a disposable
  `cabot_cup_test` database and the `TEST_DATABASE_URL` secret key.

PostgreSQL repositories for markets/wagers/settlement, the verified-result
settlement consumer, betting HTTP routes, dispatcher wiring in the server process,
S3 uploads, and the final Turso import remain follow-on implementation slices.

## 1. Outcome

Replace the separate public Cabot Cup and Cabot Book experiences with one Go web
application:

- Public visitors can browse Cup history, event photos, players, match results, and
  statistics without an account.
- Invited members can sign in, submit and confirm match results, view balances and
  ledger history, browse markets, and place wagers.
- Admins can manage invitations, players, events, teams, matches, media, markets,
  disputed results, wager approvals, and manual future/prop settlement.
- Verified match results automatically update statistics and settle linked match
  wagers exactly once.
- PostgreSQL is the authoritative store, with point-in-time S3 backup through the
  existing CloudNativePG/Barman infrastructure.
- The old public application and Turso-backed betting deployment remain available as
  rollback sources until migration and production acceptance are complete.

The Masters pool remains a separate application and is not part of this work.

## 2. Confirmed Decisions

| Area | Decision |
| --- | --- |
| Application | Evolve `go-book`; deprecate `cabot-cup` only after cutover |
| UI | Server-rendered Go templates with HTMX and minimal focused JavaScript |
| Runtime shape | Modular monolith with durable background event processing |
| Event transport | PostgreSQL transactional outbox; no external broker initially |
| Database | New `cabot_cup` database and role on the existing CNPG cluster |
| Public access | Stats, history, match results, and photos are unauthenticated |
| Authentication | Invitation/approval plus Google OIDC; provider-neutral model |
| Result verification | Opponent/captain confirmation with admin override/dispute flow |
| Match wagers | Automatically settled from verified match results |
| Futures/props | Explicit admin settlement |
| Money | Real money; integer cents and double-entry ledger |
| Photos | Direct admin-to-S3 uploads using presigned requests; CloudFront delivery |
| Historical stats | Import JSON aggregates as legacy snapshots unless raw matches exist |
| Turso | Read-only CLI access confirmed; consistent exports stay outside Git |

Day-two capabilities include an additional identity provider and, only if justified,
splitting workers or adding a message broker. True multi-leg parlays are also deferred:
the current `parlay` page only lists markets and does not implement parlay placement or
settlement.

## 3. Current-State Findings

### Public application (`cabot-cup`)

- Node/Express/EJS, one process, no tests, and no live persistence.
- Reads 23 player aggregate records from checked-in `data.json`.
- Contains narratives for 2019-2024 and 25 CloudFront photo references.
- Statistics are hand-maintained totals rather than derivations from match records.
- Production is running one pod at `cabotcup.ca`.

### Betting application (`go-book`)

- Go templates and HTMX with Google login and `user`/`admin`/`root` roles.
- Turso/libSQL stores users, markets, selections, wagers, transactions, and sessions;
  no schema migrations are present in the repository.
- Supports matchup, future, and prop markets, wager approval, manual grading,
  transactions, balance adjustments, and role administration.
- Read-only Turso access is confirmed. The live database currently contains 22 users,
  49 closed markets, 95 selections, 91 approved/graded wagers, 159 transactions, two
  market restrictions, and six sessions. All foreign-key integrity checks performed
  during discovery returned zero orphans.
- All markets and wagers are historical rather than open: wagers are 44 wins, 45
  losses, and two ties, covering activity from May 17-21, 2025.
- Legacy financial reconstruction needs explicit adjustments. User balances total
  `282.00` more than the transaction net across two users. Net wager debits after seven
  recorded refunds reconcile to retained wager stakes, but win/tie credit counts do not
  map one-to-one to current wager result counts.
- None of the 91 stored free-form wager descriptions exactly identifies a current
  selection description, so automatic foreign-key inference would be unsafe. Preserve
  original terms/descriptions and link only mappings proven during migration review.
- Current writes are not atomic across wager, transaction, balance, and settlement.
  Money uses floating point, wager rows copy free-form outcome descriptions, and a
  repeated grade can issue a duplicate payout.
- There are no automated tests. The code currently compiles, passes `go vet`, and
  reports no test files.
- The production Deployment is intentionally scaled to zero.

### Platform (`gitops` and live cluster)

- Flux/Kustomize, Envoy Gateway, cert-manager, External Secrets, AWS Secrets Manager,
  CloudNativePG 1.29.1, and Barman Cloud are healthy.
- One Kubernetes node has adequate capacity for the proposed modular monolith.
- The PostgreSQL 18 cluster is healthy; continuous WAL archiving is working and the
  three most recent weekly backups completed successfully.
- The existing `golf_pool` database has no application tables. The new Cabot database
  can be isolated by database, owner, secret, namespace, and NetworkPolicy while
  sharing the physical cluster and backup chain.
- Current retention is a 14-day recovery window with weekly base backups. That is a
  platform risk decision to revisit before real-money production.

## 4. Target Architecture

```text
Browser
  |
  v
Envoy Gateway / TLS
  |
  v
Cabot Go application
  +-- public web + HTMX handlers
  +-- invitation/OIDC/session handlers
  +-- competition and result services
  +-- market/wager/ledger services
  +-- admin and media services
  +-- outbox dispatcher + idempotent consumers
  |
  +--------------------+--------------------+
  v                    v                    v
PostgreSQL          S3 uploads          Google OIDC
(source of truth,   (presigned)         (first provider)
ledger, outbox)        |
                       v
                   CloudFront
```

The web server and worker can run in the same binary and Deployment initially. Worker
leases/row locks prevent duplicate ownership, and business idempotency makes duplicate
delivery harmless. The worker can later become a separate command/Deployment without
changing event contracts.

Suggested application boundaries:

```text
cmd/cabot/                 process startup, configuration, graceful shutdown
internal/identity/         invitations, identities, memberships, sessions
internal/competition/      events, teams, players, matches, result verification
internal/statistics/       derived projections and legacy snapshots
internal/media/            upload authorization and media metadata
internal/betting/          markets, selections, wager state machine, settlement
internal/ledger/           accounts, balanced postings, reconciliation
internal/audit/            immutable privileged-action records
internal/events/           outbox, event envelopes, dispatcher, consumers
internal/web/              routes, middleware, templates, HTMX responses
migrations/                ordered PostgreSQL migrations
web/                       templates and bundled static assets
```

## 5. Core Data Model

Use UUIDs for externally visible identifiers, `timestamptz` in UTC, explicit enums or
checked state values, and `bigint` cents for money. Important tables are grouped below;
names may change during implementation, but the ownership and invariants should not.

### Identity and access

- `users`: display profile and status, not provider credentials.
- `auth_identities`: provider, immutable provider subject, verified email metadata.
- `invitations`: hashed token, intended email, role, expiry, issuer, consumed time.
- `memberships` or role grants: role, grantor, validity/revocation history.
- `sessions`: hashed token, user, expiry, rotation/revocation metadata.
- `players`: public golf identity, optional linked user, aliases, profile photo.

### Competition, results, and statistics

- `events`: year, venue, format, narrative, lifecycle.
- `teams` and `event_team_memberships`: captains and rosters for an event.
- `matches`, `match_sides`, and `match_participants`: scheduled format, players, points,
  and two competing sides; support singles and doubles without special columns.
- `result_submissions`: proposed result, submitter, evidence/note, and state.
- `result_confirmations`: confirmer, decision, comment, and timestamp.
- `verified_results`: one authoritative result per match plus verification method.
- `legacy_stat_snapshots`: imported aggregate values, source, as-of label, and batch.
- `player_stat_projections`: rebuildable values derived only from verified matches.
- `media_assets` and associations: S3 key, metadata, publication state, uploader, event,
  match, or player link.

Important result rules:

- A submitter must be a participant, captain, admin, or owner.
- A non-admin submission needs confirmation by the opposing side or its captain.
- Conflicting submissions move the match to `disputed`; no stat or betting settlement
  occurs until resolution.
- Verification writes the authoritative result and `MatchResultVerified.v1` to the
  outbox atomically.
- Correcting a verified result creates a versioned supersession/audit record and
  triggers reversal/re-settlement rather than overwriting history.

### Markets and wagers

- `markets`: type (`match`, `future`, `prop`), lifecycle, close time, optional match.
- `selections`: immutable identity within a market, display terms, current offered
  American odds, and optional semantic result mapping for match markets.
- `market_restrictions`: members prohibited from a market, with reason and actor.
- `wagers`: member, selection, integer stake, locked odds/terms, state, timestamps,
  approval data, and idempotency key.
- `market_settlements`: outcome, source result/manual actor, reason, and version.
- `wager_settlements`: wager result, payout/refund ledger transaction, and uniqueness
  that guarantees one active settlement version.

Market edits never alter an accepted wager snapshot. Closing/settling a market and
settling all accepted wagers runs through an idempotent service. Match markets consume
verified-result events; future/prop settlement uses the same service through an admin
command.

### Financial ledger

- `ledger_accounts`: user cash, user free play, wager escrow, house/clearing, and
  migration accounts.
- `ledger_transactions`: type, currency, source, idempotency key, actor, reason, and
  reversal linkage.
- `ledger_postings`: account and signed cents; postings balance to zero per transaction
  and currency.
- Optional `account_balance_projections`: transactionally maintained/rebuildable cache,
  never a replacement for postings.

Example flows:

1. Accept wager: debit user available cash, credit wager escrow.
2. Winning settlement: debit escrow/house as defined, credit the user's cash for stake
   plus winnings.
3. Loss: move escrowed stake to the house/clearing account.
4. Void/tie: return stake from escrow to user cash.
5. Correction: reverse the original balanced transaction, then post the corrected one.

Define one integer payout/rounding algorithm and test boundary values for positive and
negative American odds. Store the accepted odds and computed settlement inputs so a
payout can always be reproduced.

### Events and audit

- `outbox_events`: event ID, aggregate ID/version, event type/version, JSON payload,
  occurrence time, availability time, attempt count, completion/failure metadata.
- `event_receipts`: consumer plus event ID uniqueness for idempotency when needed.
- `audit_entries`: actor, action, target, request/correlation ID, reason, and safe
  before/after metadata for privileged operations.

Initial event catalog:

- `MatchResultSubmitted.v1`
- `MatchResultDisputed.v1`
- `MatchResultVerified.v1`
- `MatchResultCorrected.v1`
- `PlayerStatisticsProjected.v1`
- `MarketClosed.v1`
- `MarketSettlementRequested.v1`
- `MarketSettled.v1`
- `WagerAccepted.v1`
- `WagerSettled.v1`
- `LedgerTransactionPosted.v1`
- `MediaPublished.v1`

Only publish an event when another module or operator benefits from it. Events are not
a substitute for ordinary function calls inside one transaction.

## 6. User Experience Scope

### Public

- Home/current event overview with clear navigation into history and stats.
- Year/event pages with narrative, teams, results, and gallery.
- Player directory and profile pages with legacy and event-derived stats clearly
  labeled where their provenance differs.
- Sortable/filterable statistics for overall, singles, doubles, and team records.
- Accessible responsive layouts, meaningful photo alt text, useful empty/error states,
  and no login wall around public data.

### Member

- Invitation acceptance and Google sign-in.
- Available match, future, and prop markets with terms, close time, odds, stake entry,
  approval state, and deterministic potential payout.
- Current balance and immutable ledger/transaction history.
- Open, pending, settled, voided, and rejected wagers.
- Submit a match result and confirm/reject an opponent's submission.

### Admin/owner

- Operational dashboard focused on pending confirmations, disputes, markets needing
  manual settlement, wager approvals, outbox failures, and reconciliation issues.
- Manage invitations, roles, player-account links, events, rosters, matches, and
  markets.
- Upload and publish S3 media, captions, and associations.
- Resolve results with required notes; manually settle futures/props; void markets.
- Post controlled ledger adjustments/reversals with reason and previewed postings.
- Export auditable wager, settlement, and ledger reports as CSV.

## 7. Delivery Phases And Gates

### Phase 0: Preserve and baseline

Deliverables:

- Record route, content, image, player-total, and current betting feature inventories.
- Export/hash the public JSON and capture all legacy photo metadata.
- Add CI for formatting, tests, vet/static checks, container build, migration checks,
  and Kubernetes rendering.
- Establish architecture decision records for ledger model, outbox semantics, result
  verification, auth/session model, and shared CNPG use.

Gate: repeatable baseline checks exist, and no legacy deployment or data source has
been modified.

### Phase 1: Application and database foundation

Deliverables:

- Restructure `go-book` into modular boundaries while keeping a runnable application.
- Add typed configuration, structured logging, request IDs, graceful shutdown, health
  endpoints, and a PostgreSQL connection pool.
- Select and add an established migration tool with embedded, ordered migrations.
- Create the initial identity, competition, betting, ledger, audit, and outbox schema.
- Implement the outbox dispatcher, retry/failure handling, idempotent receipt pattern,
  and worker metrics.
- Bundle templates/static assets and establish reusable accessible UI components.

Gate: migrations apply and roll forward on an empty PostgreSQL database; unit and
integration tests prove transaction rollback, outbox durability, and duplicate event
delivery behavior.

### Phase 2: GitOps and shared PostgreSQL provisioning

Deliverables:

- Add Cabot-specific database credentials to AWS Secrets Manager outside Git.
- Add an ExternalSecret in `databases`, a least-privileged CNPG managed role, and a
  declarative `Database` resource named `cabot_cup` with reclaim policy `retain`.
- Add a Cabot namespace access label/NetworkPolicy and distribute only its own
  `DATABASE_URL` into the application namespace.
- Add immutable-image Deployment, ServiceAccount, security context, resource bounds,
  probes, Service, HTTPRoute, and explicit network policies.
- Add a migration Job/release ordering and a non-production environment or database
  for integration/acceptance.
- Define RPO/RTO. Recommended starting point for real-money records is at least a
  30-day PITR window, a documented restore drill, and a periodic immutable financial
  export; confirm cost and shared-cluster impact before changing current retention.

Gate: the application connects only as the Cabot role; it cannot access `golf_pool`;
GitOps dry runs pass; an isolated backup restore is demonstrated before production
money is accepted.

### Phase 3: Public history, players, stats, and media

Deliverables:

- Build public event/year, player, statistics, match, and gallery routes.
- Import 2019-2024 narratives, 23 player aggregates, profile images, and 25 existing
  CloudFront references through a repeatable migration/seed path.
- Preserve source provenance and display legacy aggregates separately from stats
  derived from verified match records when necessary.
- Implement admin presigned S3 upload initiation/completion, validation, metadata,
  publication, and CloudFront URLs.

Gate: automated content/count reconciliation passes; public mobile/desktop visual and
accessibility checks pass; all legacy public URLs have an explicit preserve-or-redirect
mapping.

### Phase 4: Invitations, OIDC, sessions, and administration

Deliverables:

- Implement provider-neutral OIDC identities with Google first.
- Add expiring hashed invitations, atomic invite consumption, approval/status, and
  `member`/`admin`/`owner` authorization.
- Add hashed database sessions, rotation, logout/revocation, secure cookies, CSRF,
  login throttling, and security headers.
- Add member/player linking and audited invitation/role administration.

Gate: uninvited Google users cannot become members; provider subject identity is used;
CSRF and authorization tests cover all mutations; session/token values never appear in
logs.

### Phase 5: Match operations and verified results

Deliverables:

- Admin event, team, roster, captain, and singles/doubles match management.
- Participant/captain result submission, opposing-side confirmation, rejection,
  dispute, admin override, and corrected-result flows.
- Verified-result event emission and idempotent statistics projection/rebuild.
- Notifications in the application for work awaiting confirmation; external email or
  push notification can follow later.

Gate: concurrency tests prove two confirmations/corrections cannot create conflicting
authoritative results; disputed results do not affect authoritative stats or markets;
all overrides are audited.

### Phase 6: Real-money markets, wagers, and settlement

Deliverables:

- Double-entry accounts, posting service, balance projection, reconciliation command,
  and CSV audit exports.
- Admin market/selection management for match, future, and prop markets with member
  restrictions and close times.
- Wager placement with locked terms/odds, funds check and escrow, approval/rejection,
  idempotency keys, and member history.
- Automatic match-market settlement from verified-result events.
- Manual future/prop settlement using the same settlement engine.
- Void/tie/refund, result correction reversal/re-settlement, and controlled financial
  adjustment flows.

Gate: every transaction balances; cent/odds boundary tests pass; duplicate requests and
events cannot double-debit or double-pay; concurrent wagers cannot overspend available
funds; settlement reconciliation is zero before release.

### Phase 7: Turso extraction and migration

Read-only CLI access is available. Take a consistent immutable export at the start of
migration dry runs and another after the final write freeze; store both outside Git.

Deliverables:

- Inventory the real source schema, row counts, constraints, and data anomalies.
- Export users, markets, outcomes, restrictions, wagers, transactions, and balances;
  explicitly exclude sessions and OAuth tokens.
- Load staging tables with source IDs and migration batch IDs.
- Map legacy roles and users, preserve unmatched descriptive wagers, convert amounts to
  cents, and construct balanced opening/migration ledger transactions.
- Preserve all 91 wagers and 159 transactions with source IDs. Do not infer selection
  links from the current descriptions; link only reviewed matches and otherwise retain
  them as explicitly unlinked legacy terms.
- Explain the known `282.00` balance-versus-transaction difference across two users with
  source evidence or explicit opening-balance migration postings. Investigate the
  current-result versus payout/refund-count differences without deleting history.
- Run repeated dry runs and produce per-table counts, per-user balance reconciliation,
  accepted/unsettled wager reports, and unexplained-difference reports.
- Schedule a final write freeze, export delta/final snapshot, import, and sign-off.

Gate: every imported user balance is explained by ledger postings; all open financial
items are accounted for; unexplained differences are zero or explicitly approved and
represented as auditable migration adjustments.

### Phase 8: Parallel release and cutover

Deliverables:

- Deploy the unified application on a temporary acceptance hostname or route.
- Run owner/admin acceptance for public content, result workflow, media upload, all
  wager/settlement cases, audit export, and restore procedure.
- Put the old book into read-only mode, complete the final Turso import, and compare
  old/new balances and open wagers.
- Route `cabotcup.ca` to the unified app. Redirect `cabotcupbook.com` to the betting
  area while retaining a documented fast rollback route.
- Monitor HTTP errors, login failures, outbox lag, disputes, settlement failures,
  ledger reconciliation, PostgreSQL health, and backup/WAL status.

Gate: acceptance is signed off, final reconciliation passes, backups are current, and
rollback has been rehearsed. Keep old deployments and data read-only during the agreed
stabilization window.

### Phase 9: Deprecation

Deliverables:

- After the stabilization window, scale down and then remove the legacy `cabot-cup`
  workload and obsolete `cabot-book` deployment manifests.
- Archive final source exports and reconciliation reports under the approved retention
  policy; revoke unused Turso and old application credentials.
- Mark `cabot-cup` archived/deprecated with pointers to `go-book`; do not delete history.
- Update operational documentation and runbooks to describe only the supported path.

Gate: no production route, data dependency, credential, image automation, or rollback
requirement still references the legacy services.

## 8. Test Strategy

The risk concentration is financial correctness and concurrent workflow transitions,
so test depth must reflect that.

- Domain unit tests: ledger balancing, odds and cent rounding, state transitions,
  authorization, confirmation quorum, correction, and settlement mapping.
- PostgreSQL integration tests: migrations, constraints, row locks, overspend races,
  outbox claiming, idempotency, rollback, and projection rebuilds.
- Handler tests: full-page and HTMX responses, validation, CSRF, auth, safe redirects,
  upload authorization, and error states.
- End-to-end browser tests: key public/member/admin workflows at desktop and mobile.
- Migration tests: fixture exports, reruns, partial failure/resume, count/checksum and
  financial reconciliation.
- GitOps tests: Kustomize render, policy checks, server-side dry run, rollout health,
  migration ordering, and rollback compatibility.
- Operational exercises: worker restart with claimed events, database restore, expired
  invitations, OIDC outage, S3 failure, settlement retry, and result correction.

## 9. Operational Targets To Confirm Before Production

These do not block initial engineering, but they block real-money launch:

- RPO/RTO and backup retention (current recovery window is 14 days).
- Stabilization window before legacy removal.
- Currency scope (initial assumption: CAD only).
- Financial record and audit-log retention period.
- Who holds `owner` role and whether owner-only financial changes require a second
  approver.
- Maximum wager/auto-approval policies and whether negative available balances are ever
  permitted.
- S3 bucket/prefix, CloudFront behavior, image limits, and media retention/moderation.

## 10. Immediate Next Actions

1. Approve this plan and the initial assumptions in section 9, or record changes.
2. Start Phase 0 in `go-book`: CI, ADRs, route/content inventories, and baseline tests.
3. Implement Phase 1 schema and domain foundations against disposable PostgreSQL.
4. Add the Cabot database/role GitOps resources without changing the Masters workload.
5. Create a secure location and repeatable read-only Turso export/reconciliation tool in
   parallel; do not put credentials or exports in Git or block the public/match
   foundation on it.

The first production cutover should occur only after Phases 0-8 gates are satisfied.
