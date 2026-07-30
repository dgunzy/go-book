# Cabot Cup — site and book roadmap

Written 2026-07-30, after the 2026 cup closed. The book is live with real
members and real money, so everything here is sequenced by risk as much as by
value: the cheap, reversible wins come first and the schema changes come after
the things that make them safe to verify.

Every claim below was checked against the code or the production database.
File references are `path:line` at the time of writing.

**Status key:** ▢ not started · ◐ partly built · ✔ done

---

## 1. ✔ HTMX was never actually loaded — fixed 2026-07-30

**This is the answer to "should we switch to JS instead of HTMX?" — the site is
not using HTMX today.**

- Seven templates carry 16 `hx-post` / `hx-target` / `hx-swap` attributes
  (`book_markets.gohtml:28`, `admin_markets.gohtml`, `admin_wagers.gohtml`,
  `admin_settle_up.gohtml`, `admin_member_book.gohtml`, `admin_wager_record.gohtml`,
  `book_wagers.gohtml`).
- The server side is built and working: `bettingweb.go:1127` implements
  `isHTMX()` against the `HX-Request` header, three handlers branch on it
  (`:683`, `:1101`, `:1111`), and `betting_partials.gohtml` defines the
  partial they return.
- **The htmx library is not bundled and no page loads it.** `private_layout.gohtml:9`
  loads only `site.js`. There is no `htmx.min.js` in `web/static/`.

Browsers ignore unknown `hx-*` attributes, so every one of those forms silently
falls back to its `action=` / `method="post"` and does a full page POST →
redirect → GET. That is exactly the full refresh you noticed. The partial
rendering path on the server has never once executed in production.

### Recommendation: bundle HTMX, do not rewrite in JS

Adding one vendored file and one `<script>` tag turns 16 already-written
attributes into working inline updates and activates a server path that is
already built and tested. A JS rewrite would throw away working server-rendered
code to reach the same place, and it contradicts the architecture decision in
`AGENTS.md` ("Do not introduce a SPA framework... Bundle production CSS, HTMX,
icons, and JavaScript with the application").

Reassess only if, after HTMX is genuinely live, specific screens still feel
wrong. Judging the architecture on behaviour it has never exhibited is the
wrong basis for a rewrite.

| | Task | Notes |
|---|---|---|
| ✔ | Vendor `htmx.min.js` into `web/static/` | ~50 KB, embedded like every other asset; no CDN (CSP forbids it) |
| ✔ | Load it from `private_layout.gohtml` | Public pages need no JS; keep them script-free |
| ✔ | Keep htmx inside the CSP | It injects inline styles unless `htmx.config.includeIndicatorStyles=false`; prefer the config flag over widening CSP |
| ◐ | Verify each of the 7 templates round-trips as a partial | The forms keep `action=`, so no-JS degrades gracefully — keep it that way |
| ▢ | Add `hx-` to the remaining ~40 full-post forms, screen by screen | Only after the first seven are proven |

### Two details that decide whether enabling it is safe

**Error responses need a target that is not the form.** The forms use
`hx-target="this" hx-swap="outerHTML"`, and `failPost` (`bettingweb.go:1109`)
returns its error fragment with a 4xx. htmx 2.x does not swap on 4xx by default,
so enabling the library as-is means a rejected wager silently does nothing —
the member gets no feedback at all. Configuring 4xx to swap is worse: the error
fragment would replace the form and there would be nothing left to retry with.
The fix is an `HX-Retarget` header on error responses pointing at a status
region beside the form, which is a small change to `fragment()` but a real one.

**`style-src 'self'` will block htmx's indicator styles.** htmx injects a
`<style>` element on load unless `includeIndicatorStyles` is false. Set it via
`<meta name="htmx-config">` rather than widening the CSP.

**There is no staged rollout.** `cabot-cup-next` (production) and
`cabot-cup-test` (acceptance) both run the image built from `main`, so pushing
deploys to both at once. "Rehearse on acceptance first" does not work for
application code the way it does for migrations — the rehearsal has to happen
locally, against a real PostgreSQL and a real browser, before the commit lands.

---

## 2. Named backlog items

### 2.1 ◐ Finish parlays — matchups only (backend done 2026-07-30, no UI yet)

**State:** schema and pure domain logic exist; nothing else does.

- `migrations/000016_parlays.up.sql` created `parlays`, `parlay_legs` and
  `parlay_settlements` — **live in production and never written to.**
- `internal/betting/parlay.go` (266 lines) and `parlayprice.go` (97) implement
  placement and grading, with 377 lines of tests.
- `internal/bettingpg` has **no** parlay store methods. `internal/bettingweb` has
  no routes. No template mentions parlays. The tables are unreachable from the
  running application.

The domain layer is the hard part and it is done, including the rules that
matter: no duplicate market per parlay (enforced by `UNIQUE (parlay_id, market_id)`
— correlated legs are how a book gets picked off), push/void legs dropping out
and repricing on the survivors, and a refusal to write a parlay priced shorter
than even money.

**Scope decision (owner): matchups only.** Restricting legs to match markets is
also the correct risk call — match markets settle from verified results, so a
parlay's legs resolve deterministically. Enforce it in the domain rather than
the UI so it cannot be bypassed.

| | Task | Notes |
|---|---|---|
| ✔ | Reject non-match legs in `PlaceParlay`, with a dedicated error | Already in the domain; now covered by an integration test |
| ✔ | `bettingpg`: place, accept, reject, resolve-leg, settle | `parlay.go`, `parlayload.go` |
| ✔ | Hook leg resolution into market settlement | Runs inside the settlement transaction |
| ✔ | Ledger: acceptance debit and settlement credit | Reuses the wager transaction types with `source_type = parlay` |
| ▢ | Approval path — parlays are exactly where a small stake becomes a large liability, so they should respect the auto-approve threshold and probably sit below it |
| ▢ | **Web: build slip, place, list on `/book/wagers`, show on the admin wagers queue** — the only thing between this and members using it |
| ✔ | Integration tests against real PostgreSQL, including a leg voiding out | Five scenarios |
| ▢ | `/admin/help` + `CONFIGURATION.md` |

**Risk:** this is the largest item on the list and the only one that moves money
in a new way. Rehearse the whole flow on `test.cabotcup.ca` before it reaches
production.

### 2.2 ✔ Do not require grading on a market nobody bet — shipped 2026-07-30

**Confirmed:** `settle.go:19-33` — `MarketOutcome.validate` requires an entry for
**every selection**, wagers or not, and `admin_market_settle.gohtml:56` marks the
outcome radios `required`. A five-way prop with zero bets still forces you to work
out and record a winner.

**Approach.** Add a distinct terminal state rather than overloading `void`:
`closed_no_action` (name TBD). Void means "this was live and is being unwound";
a market nobody touched was never at risk and should not be recorded as if it
were. A separate state keeps reconciliation and the audit trail honest.

- Allow the transition only when the market has zero accepted **and** zero
  pending wagers — assert it in SQL inside the settling transaction, not just in
  the handler, so a wager placed in a race cannot slip past.
- Match markets are exempt: they always grade through match settlement, per your
  instruction.
- Surface it in the admin queue as a one-click "no bets — close it out" rather
  than a hidden alternative to grading.

### 2.3 ✔ Total cap across all bettors on a side — shipped 2026-07-30

**Confirmed gap.** Both existing caps are strictly per-member:

- `place.go:40` — "MaxStakeCents caps what **one member** may have riding on this
  market"
- `place.go:46` — "SelectionMaxStakeCents caps what **one member** may have on
  this side"
- `MaxPayoutCents` (`place.go:52`) caps a single wager's profit.

Nothing caps the book's total exposure on a side. Ten members at the per-member
limit is ten times the intended liability. `privateweb` computes a `WorstCase`
exposure view (`private.go:145`) but it only reports; it never blocks.

**Approach.**

- Add `selections.total_stake_cap_cents` (nullable — null means no cap), and
  probably a book-wide default in `internal/config` so new markets are protected
  without remembering to set one.
- Enforce inside the placement transaction with the same row lock that already
  serialises stake accounting. A cap read outside the lock is not a cap: two
  simultaneous placements would both see room. This is the one detail that
  decides whether the feature actually works.
- Decide explicitly whether pending wagers count toward the cap. Recommendation:
  **yes** — otherwise the cap can be walked past with unapproved wagers and then
  breached the moment they are approved.
- The error must be distinguishable from the per-member limit, or members will
  be told they are over a limit they have not personally reached. Suggest
  "this side is full" rather than "your limit".
- Show remaining room on the side in the UI, the way `MaxStakeCents` already
  renders in `book_markets.gohtml:26`.

---

## 3. Public site

The 2026 work landed well: unified career table, photo gallery, downloadable
originals. What remains:

| | Item | Notes |
|---|---|---|
| ▢ | **2025 cup page** | Blocked on source material — teams, result, photos. The placeholder is honest but it is the only gap in an otherwise complete 2019–2026 archive |
| ✔ | **Open Graph / Twitter cards** | **Zero** social tags in `layout.gohtml`. A site whose main artifact is photography currently shares as a blank rectangle. Highest value-per-hour item on this list |
| ✔ | `robots.txt` + `sitemap.xml` | Shipped 2026-07-30; the sitemap builds from the archive and a test walks every URL it lists |
| ✔ | Canonical URLs | Shipped 2026-07-30; the query string is dropped so /players?sort=cups is not a second page |
| ▢ | Portraits for pre-2026 players missing them | Several legacy players still use `empty_profile.jpeg` |
| ▢ | Wide-table scroll affordance on mobile | Tables scroll correctly inside `.wide-table-wrap` but nothing signals it |
| ▢ | Per-player pages (`/players/{slug}`) | Cards are anchors only; a real page would give the career table somewhere to link and give each golfer a shareable URL |

---

## 4. Betting UX beyond HTMX

| | Item | Notes |
|---|---|---|
| ▢ | Bet slip / confirmation step | Today a stake is typed straight into a table row and submitted — no confirm, no review of price before commit |
| ▢ | Show why a wager is pending | Members see "pending" without knowing it is awaiting approval or over a limit |
| ▢ | Odds movement indicator | Dynamic pricing moves lines; nothing shows the direction of travel |
| ▢ | Empty and error states | Several tables render bare when empty |
| ▢ | Mobile pass over the admin screens | The admin tables are dense and were built desktop-first |

---

## 5. Backend and operations

Overall the backend is in good shape: 21,759 source lines against 17,377 test
lines (0.80 test:source), 17 migrations each with a `down`, and integration
suites gated behind six separate `*_TEST_DATABASE_URL` variables.

| | Item | Notes |
|---|---|---|
| ▢ | **Delete or complete the parlay tables** | Three unused tables in production is the kind of thing that gets misread later. Completing §2.1 resolves it; if parlays are shelved, drop them with a rehearsed `down` |
| ▢ | Audit `CONFIGURATION.md` against `internal/config` | 18 env vars in code; needs a line-by-line check against the doc and the GitOps ConfigMaps |
| ▢ | Verified-result correction path | Correcting a `display_score` today means direct SQL (as on 2026-07-29). The versioning chain exists for result corrections but there is no admin path for a cosmetic fix that must not re-settle |
| ▢ | Legacy `cabot-cup` and `cabot-book` deployments | `cabot-cup` still runs the retired EJS app; `cabot-book` is scaled to zero on a stale image. Both are dead weight and both are confusing in `kubectl get deploy -A` |
| ▢ | Backfill 2019–2024 as verified matches | Would let `buildCareer` merge on one source and retire the legacy/verified split entirely. Large, low urgency, and the guard in `web.buildCareer` already refuses to double count if a verified season lands on or before the 2024 cutoff |

---

## 6. Suggested sequence

**Done 2026-07-30**

- Open Graph tags, `robots.txt`, `sitemap.xml`, canonical URLs (§3)
- Zero-bet close-out (§2.2) — new `closed_no_action` state, migration 000018
- Total cap per side (§2.3) — migration 000019, enforced under `FOR UPDATE`

**Now**

1. Parlays end to end (§2.1) — the largest remaining item
2. Extend `hx-` to the remaining ~40 full-post forms, screen by screen
3. `CONFIGURATION.md` audit

**Note on the htmx swap semantics.** Success keeps the authored
`hx-target="this" hx-swap="outerHTML"`, so a control is replaced by its own
result. That is right for placing a wager and slightly odd for an admin inline
control, which disappears after use until the page is reloaded. Giving those
screens a proper partial re-render is the follow-up; it is per-screen work and
deliberately not bundled with turning the library on.

**Later — largest, do when there is room to rehearse**

7. Parlays end to end (§2.1), matchups only, rehearsed on acceptance
8. Bet slip / confirmation flow
9. Per-player pages, 2025 archive when material arrives

---

## 7. Open questions for the owner

1. **Zero-bet markets** — is a separate `closed_no_action` state right, or would
   you rather reuse `void`? Separate is cleaner for reconciliation; reuse is less
   schema churn.
2. **Total cap** — a book-wide default, or per-market only? A default protects
   markets you forget to configure.
3. **Pending wagers against the cap** — count them (recommended) or not?
4. **Parlays** — still wanted, or shelve and drop the tables? It is the largest
   item here by some distance.
5. **2025** — is there source material, or should the page say plainly that the
   year was not played?
