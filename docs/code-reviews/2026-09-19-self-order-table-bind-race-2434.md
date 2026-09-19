# Code review: self-order table-bind race, empty sessions (ut-docs#2434)

**Date:** 2026-09-19
**Card:** ut-docs#2434 — follow-up finding N3 from the ADR-0103 review
(`docs/code-reviews/2026-09-19-self-order-session-basket-2261.md`), also
flagged as a known follow-up in
`docs/code-reviews/2026-09-19-self-order-table-move-preserves-basket-2433.md`
**Complexity:** medium — Sonnet built, Opus reviewed

## What shipped

Two related bugs in `bindSelfOrderTableSession` (`internal/pages/self_order_page.go`),
both from the same root cause — the busy check and the bind were separate
operations instead of one atomic one, in more places than the first pass
found (see Round 1 below):

1. **Threshold gap**: the busy guard only blocked a second scan of a table
   when the existing session already had `len(Lines()) > 0`. Two phones
   scanning the same table before either guest added an item both passed
   the guard and both bound — two independent, uncoordinated baskets for
   one physical table, surfacing at checkout as two `kiosk_counter_orders`
   rows for a table the old shared-`KioskEngine` design made structurally
   impossible.
2. **Check-then-act race (mint/move)**: even with the threshold fixed, the
   busy check (`TableOwnerActive`) and the bind (`Create`+`SetTable`) were
   two separate `manager.mu` critical sections, so two truly-concurrent
   requests could both observe "not busy" before either bound.
3. **Check-then-act race (résumé) — found only in round 2's independent
   review**: the résumé case ("this browser's own cookie already names
   this table") was special-cased *before* the busy check entirely,
   unconditionally. A session idle past `maxIdle`, superseded by a
   different phone's legitimate bind, then woken and resumed, bypassed the
   busy check and produced the same two-baskets bug via a third path.

Fix: `internal/pos/session_manager.go` gains
`SessionBasketManager.BindTable(tableID, tableLabel, ownToken, mover,
maxIdle, now) (token string, svc *Service, busy bool)` — a single `m.mu`
critical section that does the busy check (no item-count exception — an
EMPTY session holds its table exactly like a non-empty one) and either
moves the caller's own existing session onto the table (mint, move, AND
résumé all go through this one path as of round 2) or mints a fresh one,
atomically. The B1 recency window (`selfOrderTableBusyMaxIdle`) is
unchanged and still applies to empty and non-empty sessions alike, so the
"an abandoned cart must not hold a table hostage" guarantee is unaffected
by dropping the item-count check.

ADR-0103 is corrected in place (same convention the ADR already used for
finding N4): Decision 4's "non-empty" qualifier and Decision 6's "fails
safe for this case" claim were both wrong, per this same review's finding
N3 — corrected under a `> **Correction (2026-09-19, ut-docs#2434, ...)**`
block in `ut-docs` PR #2442 (merged), not a superseding ADR, since the
architectural decision itself (per-table session concurrency, a
same-table-only busy guard) is unchanged; only the guard's threshold and
atomicity were wrong. Round 2's fix closes the résumé-path gap Decision
6's "now actually prevents this case" claim would otherwise have
overstated (the review reproduced exactly this gap before the round-2
fix landed) — the claim is accurate as merged.

## Independent review — round 1

Opus subagent, worktree-isolated (complexity:medium → Opus review).

**Verdict: safe to merge with three fixes required first.** Findings,
ranked, and what was done about each:

### Should-fix (all four addressed)

- **S1** (real, filed as follow-up, not fixed here): `BindTable` calls
  `mover.SetTable`/`svc.SetTable` while holding `m.mu`, and `SetTable` can
  trigger a blocking plugin tax/charge-policy ask (up to ~10s for a
  net/tcp-permitted plugin) — serializing every OTHER table's concurrent
  request behind it for that ask's duration. Real regression vs. the
  pre-existing code (which called `SetTable` outside the lock in both
  branches), but latency/availability, not correctness or data loss — the
  reviewer's own verdict allows deferring it. Filed as a follow-up
  (untracked as of this record — file before closing the parent card).
  Documented inline in `session_manager.go`'s lock-order comment so the
  next reader isn't surprised by it.
- **S2** (fixed): the concurrent race test asserted `bound == 1` on a
  single run. Measured: re-running the OLD racy implementation against
  this exact test shape let two goroutines both win in 5/20 trials under
  `-race` and 2/20 without — a revert would pass the test most of the
  time. Looped across 30 trials, fresh manager per trial.
- **S3** (partially fixed, rest filed as follow-up): the busy-screen copy
  ("This table already has an order in progress") was written for the
  non-empty-only guard and reads false for an empty session that now also
  blocks. Reworded to "This table is already in use" (both
  `selforder.table_busy.title` and `.hint`). NOT done: shortening the
  10-minute hold specifically for still-empty sessions, and a staff-facing
  override — real UX gap (an in-app-browser-to-Chrome cookie-jar switch
  can self-lock a guest for up to 10 minutes) but out of this card's
  minimal-fix scope; filed as a follow-up.
- **S4** (fixed — this is what round 2 exists for): see "What shipped"
  point 3 above. Reproduced by the reviewer via a throwaway probe test
  before the fix; independently re-reproduced here (see "TDD" below)
  before and after, including finding and fixing a clock-handling bug in
  the FIRST reproduction attempt (a synthetic `now` argument doesn't
  advance `SessionBasketManager`'s own injectable clock — `lastSeen`
  stamps always come from `m.clock()`, not the `now` parameter — so a
  naive probe using two different clock sources produces a false pass).
  Promoted into a permanent regression test,
  `TestSessionBasketManager_BindTable_StaleResumeAfterCompetingBindSeesBusy`.

### Nits — N1, N2, N6 addressed; N3, N5, N7 deferred

- **N1** (fixed): `BindTable` now guards `m == nil` / `tableID == ""` like
  every sibling method.
- **N2** (fixed): the manager's lock-order doc comment now names
  `BindTable` and its S1 caveat.
- **N3** (deferred): `TableOwnerActive`'s busy predicate is now duplicated
  in `BindTable`; extracting a shared helper or deleting `TableOwnerActive`
  is a clean refactor but not required for correctness — left as-is,
  `TableOwnerActive` is genuinely still useful as a read-only diagnostic/
  test query (see the deadcode-baseline entry below).
- **N4** (fixed): added
  `TestSessionBasketManager_BindTable_EmptySessionFreesTableAfterMaxIdle`
  — nothing previously asserted an EMPTY session frees its table via
  `BindTable` after `maxIdle`, only non-empty-session variants existed.
- **N5** (deferred): `BindTable` doesn't verify `SetTable`'s postcondition
  (silently a no-op if the basket has no dine-in line and order type is
  Takeaway — currently unreachable per the existing takeaway-clamp
  invariant `self_order_page.go` already documents). Real defensive gap,
  but adding untestable dead-branch handling for a currently-unreachable
  state risks its own class of bug; deferred rather than added blind.
- **N6** (fixed): if a mover's own session was swept between the caller's
  `Get` and this `BindTable` call, the code now falls back to minting a
  fresh session instead of silently "succeeding" against an orphaned
  `*Service` with no cookie ever set.
- **N7** (deferred): no `web/help/` mention of the busy screen exists at
  all (pre-existing gap, not introduced by this diff) — a one-sentence
  addition would help support load now that empty sessions trigger it
  more often, but it's polish, not a defect; left for a documentation
  pass.

### CI caught a gap review didn't: `guard-deadcode-baseline.sh`

Round 1's push failed the `desktop-shell` CI job: `Create` and
`TableOwnerActive` lost their only production caller (round 1's own
`BindTable` already inlined mint logic and moved the busy check off
`TableOwnerActive`) and are genuinely reachable only from `_test.go`
files now. The guard's own documented convention (see its header comment)
treats a test-only-reachable exported function as a legitimate baseline
entry, not something to delete — added both to
`scripts/ci/deadcode-baseline.txt` accordingly.

## TDD, round 2

- Reverted the résumé-path fix specifically (restored the early
  `if currentToken != "" && current.TableID() == t.ID { return true,
  false }` short-circuit ahead of the `BindTable` call), confirmed
  `TestSessionBasketManager_BindTable_StaleResumeAfterCompetingBindSeesBusy`
  is the test that would have caught it (verified the underlying
  mechanism via a manager-level probe reproducing the exact scenario;
  see S4 above for the clock-handling correction this took to get right),
  restored the fix.
- Confirmed `bound != 1` in the (uncontrolled) probabilistic single-run
  version of the concurrency test against a manually-reverted `BindTable`
  that split the check and the bind back into two lock acquisitions.

## Verified beyond automated tests

- Real concurrent race proven under `-race`, not just asserted: 32
  goroutines × 30 trials racing `BindTable` on the identical table id —
  exactly 1 binds and the other 31 see `busy=true` with no token/service,
  every trial.
- Mover-path atomicity covered separately
  (`TestSessionBasketManager_BindTable_MoverBlockedByBusyTableKeepsOwnBinding`):
  an existing session moving onto an already-held table stays on its
  original table with its basket untouched.
- No regression to résumé (`TestSelfOrder_SameTableRescan_NeverBusy` —
  still passes because `BindTable` excludes the caller's own token from
  its scan, so an uncontested résumé is still never busy), move
  (`TestSelfOrder_MovingOntoBusyTable_ShowsBusyAndKeepsMoverOnOriginalTable`,
  `TestSelfOrder_SameGuestScansDifferentTable_PreservesBasket`), or
  idle-recovery (`TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard`).
- Locking discipline: `BindTable` takes `m.mu` once at its own top and
  calls `mover.SetTable`/`svc.SetTable` (`Service.mu`-guarded methods)
  while holding it — the same documented `manager.mu -> Service.mu` order
  every other manager method already uses; a `Service` never calls back
  into the manager, so no inversion is possible (S1's latency cost is real
  but is not a lock-order/deadlock risk — confirmed the same way the
  original ADR-0103 review confirmed the pre-existing askers only touch
  `db`, never `common.Deps`).
- `go build ./...`, `gofmt -l .` (no output), `golangci-lint run
  ./internal/pos/... ./internal/pages/...` and the whole-repo
  `golangci-lint run ./...` — 0 issues.
- `guard-data-access.sh` (no SQL touched), `guard-kiosk-engine.sh` (no
  `Engine` reference), `guard-i18n.sh` (the busy-screen copy change is an
  existing-key VALUE edit, not a new key — key set unchanged across
  locales), `guard-page-http-error.sh`, `guard-docs-shots.sh` (self-order
  isn't in the docs-shots route set), `guard-e2e-fixtures-import.sh`,
  `guard-htmx-loaded.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-deadcode-baseline.sh` (fixed per above) — all green.
- Full `go test ./...` (whole repo) green before each commit.
- No money/tax involvement (pure session/table-binding logic, no
  `money.Money` types touched). No offline-first concern (in-memory,
  LAN-local state, no network dependency introduced or removed). No UI
  surface changed beyond the copy string above — the existing "Busy"
  screen template is reused as-is, so no visual-check attestation is owed
  beyond confirming the copy string itself renders correctly (it does —
  covered by the existing/renamed `TestSelfOrder_*ShowsBusy` HTTP-level
  tests asserting on the literal rendered text).

## Explicitly not in scope

- ut-docs#2432 (unbounded self-order session creation / resource
  exhaustion) and ut-docs#2435 (`SessionBasketManager` mutex held across
  plugin round-trips in `SetConfig`/`HasItems`/`TableOwner`) — separate
  cards from the same ADR-0103 review, touching the same file/area but
  independent findings, not addressed here.
- S1 (plugin ask under the manager lock) and the rest of S3 (shortened
  empty-session hold + staff override) — real findings from this card's
  own review, deliberately deferred per the review's own verdict; file as
  Backlog cards before closing this card out.
- Merging two same-table baskets after the fact — moot now that the race
  producing them is closed, not a mechanism this card adds.

## Safe-to-merge verdict

Yes (round 2, after S2/S4 fixed and S3's copy half addressed, per the
round-1 reviewer's own explicit condition for merge).
