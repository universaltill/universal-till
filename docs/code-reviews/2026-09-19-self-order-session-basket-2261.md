# Review: per-table self-order session baskets (ADR-0103, universaltill/ut-docs#2261)

**Card:** ut-docs#2261 (split from ut-docs#815), `complexity:hard`. Concurrent
multi-table guest ordering via QR: #815's own review shipped a fail-*safe*,
not fail-concurrent, mitigation (a cross-table "till busy" guard on the
single shared `KioskEngine` basket) and explicitly deferred real
per-session concurrency to this card. This card closes that gap.

**Process:** Dev resumed from a prior dead cycle's WIP checkpoint (branch
`feat/2261-self-order-session-basket`, one commit, already build/test/lint
clean before this review — rebased onto 12 newer `main` commits with one
trivial `web/help/img/manifest.json` conflict). Independent review at
**Opus**, isolated worktree, per this card's `complexity:hard` routing
(Dev tier is cheap, Review tier is strong — never invert). Two rounds run:
the first found two blockers and one docs erratum; a second, scoped round
verified the fix commit. Model routing followed throughout: Dev/orchestration
at Sonnet, both review rounds at Opus.

## What shipped

- `internal/pos/session_manager.go` (new): `SessionBasketManager` — a
  `sync.Mutex`-guarded map holding one independent `*pos.Service` per
  table-bound guest session, keyed by a 16-byte `crypto/rand` hex token
  (same shape `internal/data/order_tracking_repo.go`'s tracking token
  uses). `Create`/`Get`/`Remove`/`TableOwner`/`Sweep`/`SetConfig`/`HasItems`,
  all nil-receiver-safe.
- `internal/pages/self_order_page.go`: `selfOrderSession`/`selfOrderEngine`
  resolve "this request's basket" from an `HttpOnly`, `SameSite=Lax`
  session cookie (`Path: "/"` — deliberately, not `/self-order`, since
  `/api/self-order/*` doesn't share that prefix; see the N4 finding
  below). `bindSelfOrderTableSession` mints/resumes/blocks per ADR-0103
  Decision 2/4.
- `internal/pages/self_order_shop.go`: the ~30 existing
  `d.KioskEngine.X()` call sites under `/api/self-order/*` become
  `selfOrderEngine(d, r).X()` — mechanical, no handler logic redesigned.
  Checkout removes a table session outright (not a `Reset`) so the table
  frees immediately on completion.
- `internal/pages/self_order_session_sweep.go` (new) + `init.go`: a
  background loop evicts sessions idle >2h, wired alongside the other
  `Start*` loops and joined by `app.Run`'s shutdown drain. Settings-save
  paths that already push config to `KioskEngine` now also push it to
  every live session (`SelfOrderSessions.SetConfig`).
- `guard-kiosk-engine.sh` stays green unmodified — no site starts or stops
  referencing `d.Engine`.
- README updated (table-side ordering moved from "not built" to "built",
  with the real constraints stated: LAN-only, in-memory, no cross-device
  resume).
- Tests: `internal/pos/session_manager_test.go` (9 cases, including a real
  concurrent test, `TestSessionBasketManager_ConcurrentSessionsDoNotInterfere`)
  and extensive `internal/pages/self_order_table_test.go` /
  `self_order_table_checkout_test.go` handler-integration coverage
  (`TestSelfOrder_TwoTables_BasketsNeverCross`,
  `TestSelfOrder_SameTableHeldByAnotherSession_ShowsBusy`, session
  lifecycle, idle sweep, checkout paths), each driven through a real
  `net/http/cookiejar` per simulated guest so RFC 6265 path-matching is
  exercised for real, not mocked.

## Independent review — round 1 (full diff)

Ran the six required checks (gofmt, build, vet, full test + `-race` on
every touched package, `golangci-lint`, `guard-kiosk-engine.sh`,
`guard-i18n.sh`) — all green, zero `DATA RACE` reports. One tooling note
recorded for the pipeline: the first `-race` run on `internal/pages`
reported a bare `FAIL ... 600.179s` with no race — that is `go test`'s
default 600s per-package timeout, not a race; `-race` makes this
DB-heavy package ~13x slower (82s → 1093s with `-timeout 45m`).

**TDD-verification of the two most load-bearing new tests**
(`TestSelfOrder_TwoTables_BasketsNeverCross`,
`TestSelfOrder_SameTableHeldByAnotherSession_ShowsBusy`): reviewer
temporarily hand-edited `selfOrderSession` to always return
`(d.KioskEngine, "")` — the old single-basket design — and confirmed both
tests now FAIL for the right reason (a cross-table basket bleed; the busy
guard reading the wrong, always-empty shared engine instead of the real
owning session), then reverted and confirmed both pass again. Real
assertions, not tautologies.

**Findings:**

| # | Severity | Summary | Outcome |
|---|---|---|---|
| B1 | Blocker | An abandoned session held its table "busy" for the full 2h idle-sweep window, no way to clear sooner | **Fixed**, see below |
| B2 | Blocker | Busy-screen copy said "another table"/"this device" — both wrong after the cross-table→same-table narrowing | **Fixed**, see below |
| N4 | Should land before merge | ADR-0103 §Decision 2 states `Path: "/self-order"`; code correctly uses `Path: "/"` (the narrower value would silently break every `/api/self-order/*` call) | **Fixed** — `ut-docs` PR #2431, corrects the ADR text in place (not a superseding ADR: the architectural decision is unchanged, only an implementation-detail value in the prose was wrong) |
| N1 | Non-blocker | Unbounded session creation on an anonymous LAN route — resource-exhaustion surface | Backlog: ut-docs#2432 |
| N2 | Non-blocker | Scanning a different table silently discards the guest's in-progress basket (the one zero-coverage line in the diff) | Backlog: ut-docs#2433 |
| N3 | Non-blocker | Two phones scanning one table before either adds an item can both bind — nondeterministic busy/not-busy on a later scan | Backlog: ut-docs#2434 |
| N5 | Non-blocker | `SessionBasketManager`'s mutex held across plugin round-trips in `SetConfig`/`HasItems`/`TableOwner` — not a deadlock (verified: the plugin askers never re-enter the manager), a latency concern | Backlog: ut-docs#2435 |

Also explicitly checked and confirmed clean: concurrency/lock-ordering
(no inversion possible — traced the asker closures), the sweep loop's
`wg`/`ctx` shutdown-drain wiring, the "mechanical rewrite" claim for the
~30 `KioskEngine` call sites (verified site-by-site, not trusted from the
commit message), `guard-kiosk-engine.sh`'s static-analysis scope, rebase
hygiene (no stray conflict markers), and checkout lifecycle ordering
(`releaseSelfOrderSession` always after a *successful* `completeTender`,
before any body write).

## Fix commit (B1/B2/N4)

- **B1** — `internal/pos/session_manager.go` gains
  `TableOwnerActive(tableID string, maxIdle time.Duration, now time.Time)`,
  `TableOwner` narrowed to a session whose `lastSeen` is within `maxIdle`
  of `now`. `bindSelfOrderTableSession` now takes an explicit `now
  time.Time` (the real handler passes `time.Now()`; tests pass whatever
  they need, same injectable-time shape `Sweep` already established) and
  calls `TableOwnerActive(t.ID, selfOrderTableBusyMaxIdle, now)` — a new,
  deliberately much shorter 10-minute constant — instead of the old
  unfiltered `TableOwner`. The abandoned session itself is untouched:
  still live, still resumable by its own cookie, still evicted only by
  the real 2h `Sweep`; only a *different* browser's scan stops treating
  it as "holding" the table once it goes quiet. New tests:
  `TestSessionBasketManager_TableOwnerActiveIgnoresStaleSessions`
  (`internal/pos`), `TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard`
  (`internal/pages`).
- **B2** — rewrote `selforder.table_busy.{title,hint}` in all four core
  locales (en/ar/fa/tr — no new keys, so no lang-pack-drift/follow-up
  needed) to describe the real, narrowed condition (same table, a
  different device) instead of the stale cross-table/single-device
  claim, and updated the template's explanatory comment
  (`web/ui/pages/self_order.html`). Every test asserting on the old
  literal string `"This till is busy"` was updated to the new title —
  checked explicitly so a stale literal couldn't silently make an
  assertion vacuous.
- **N4** — `ut-docs` PR #2431 (separate repo, merged
  2026-09-19: `71167a3`).

**Round-2 scoped re-review** (Opus, isolated worktree, fix commit only —
not a re-review of the already-cleared round-1 diff): re-ran the six
checks against the fix, re-verified B1 via the same revert/restore TDD
method (bypass `TableOwnerActive`, confirm
`TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard` fails, restore,
confirm it passes — it failed on the load-bearing assertion, not
incidentally), and confirmed `selfOrderTableBusyMaxIdle` (10m) and
`selfOrderSessionMaxIdle` (2h, unchanged) stay genuinely independent
thresholds with no stale `"This till is busy"` literal surviving anywhere
in the test suite (checked via positive-assertion tests, ruling out a
silently-vacuous check). **Verdict: safe to merge.**

Two small new findings from round 2, both addressed:

- **F1** — `TableOwner`'s own doc comment claimed "two sessions bound to
  one table" was unreachable in practice; B1's fix makes it reachable
  (an old, idle-but-not-yet-evicted session plus a fresh one a second
  phone just minted). Not a functional bug — `TableOwner`'s only caller
  left is a test, and `TableOwnerActive`'s busy-guard caller treats either
  as busy regardless of which one a map iteration happens to return — but
  exactly the "comment stops describing the code" class B2 itself was
  filed for. Fixed in place (comment-only).
- **F2** — the external `ut-plugin-language-de`/`ut-plugin-language-es`
  packs still carry the *old* `selforder.table_busy.*` text: B2 only
  touched core's four locales, and `lang-pack-drift` is key-parity only
  (blind to a changed value on an existing key). Filed as its own backlog
  card, ut-docs#2436, rather than pulled into this cycle — translating
  into two more repos' packs is real, bounded follow-up work, not a
  reason to hold this fix.

## Verified beyond automated tests

- `go build`/`go vet`/`gofmt` clean; full `go test ./...` green; `go test
  -race` clean (zero data races) on every touched package, both before
  and after the fix commit.
- `golangci-lint run ./...` — 0 issues, both commits.
- CI-blocking guards re-run locally and green: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (only pre-existing,
  already-baselined drift — nothing new), `guard-docs-shots.sh` (surface
  hash refreshed via `update-docs-shots-surface-hash.sh` — comment/copy
  changes only, the docs-shots harness never visits `/self-order`, so no
  screenshot regeneration is needed), `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-page-http-error.sh`. (`shellcheck` binary
  unavailable in this sandbox — a pre-existing environment gap, not a
  regression from this change.)
- No real client/shop name used anywhere in code, tests, or copy.

## Safe to merge

Yes, once round-2's scoped verification confirmed the B1/B2 fixes are
correct and complete and introduced no new regressions. N4 is fixed in
its own repo/PR. N1/N2/N3/N5 are accepted, real follow-up work, filed as
their own backlog cards rather than scope-creeping this one — none of
them changes the correctness of what ships here.

## Addendum: stale-PR sweep, merge conflict, CI red (2026-09-19, lane:cloud-24)

A later cold cycle found this PR sitting reviewed-but-unmerged
(`status:in-review`, review record already present) with `mergeable_state:
dirty` against a `main` that had moved (PRs #1293/#1294 landed since). Per
`PR-SWEEP.md`'s stale-PR-first handling:

- Claimed the PR (issue-comment marker) before touching anything.
- Merged `main` into the branch. One conflict, in the generated
  `web/help/img/manifest.json` (both sides had changed `surface_sha256` —
  a pure value collision, not a content merge) — resolved by regenerating
  it for real via `make docs-shots` (124 screenshots, ~3.7 min) rather than
  hand-picking either side's stale hash, per this repo's "regenerate
  generated files with the repo's own tooling" convention.
- Pushed the merge commit; `go build ./...` clean.
- Real CI then failed on `desktop-shell`'s deadcode-baseline guard step
  (`-tags=desktop`, full three-root analysis, which only real CI's
  GTK/WebKit-header runner can execute): a new, genuinely unreachable
  function, `internal/pos/session_manager.go: SessionBasketManager.TableOwner`.
  Confirmed `desktop-shell` was green on `main` immediately before this
  push, so this was this PR's own regression, not a base-branch flake —
  worked it, not deferred or ported.
  - Traced it: `TableOwner` (the unfiltered lookup) was narrowed to
    `TableOwnerActive` (the recency-filtered one) for the busy-guard's
    production call path by this PR's own review finding B1 above, but
    the unfiltered method itself was deliberately kept — its extensive doc
    comment documents the underlying "any session bound to this table"
    semantics `TableOwnerActive` narrows from, and `internal/pages/
    self_order_table_test.go` (a different package) calls it directly to
    test that raw semantics, alongside `internal/pos/session_manager_test.go`'s
    own direct unit tests. Grepped every call site: genuinely zero
    production callers, only the two test files above — exactly the
    guard's own documented "exported helper used exclusively by its own
    tests" false-positive shape (its comment cites `ResetCacheForTests` as
    the precedent).
  - This is a legitimate test-only-reachable case, not dead code to
    delete — deleting `TableOwner` would mean losing direct unit coverage
    of the manager's raw table-binding storage, independent of the busy
    guard's recency policy layered on top in `TableOwnerActive`. Added
    `internal/pos/session_manager.go: unreachable func:
    SessionBasketManager.TableOwner` to `scripts/ci/deadcode-baseline.txt`
    (alphabetically placed, exact string match to the guard's own output).
  - Verified: `gofmt -l .` clean, `go build ./...` clean, `go vet ./...`
    clean, `go test ./internal/pos/... ./internal/pages/...` all green.
    Could not re-run the `desktop-shell` deadcode step itself locally (no
    GTK/WebKit headers in this sandbox — the same gap ut-docs#2425 exists
    to narrow, tracked separately) — the baseline-entry fix was verified
    by exact string match against the real CI failure's own output, and
    left for CI's next run on this branch to confirm end-to-end.
- Re-verified lane ownership (`SKILL.md` rule 7a) before this and stays
  the same throughout: no independent fix for this card landed elsewhere,
  no supersession found in `main`'s log or the issue's comments.
