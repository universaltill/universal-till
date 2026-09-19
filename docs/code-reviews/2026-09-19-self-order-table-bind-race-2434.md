# Code review: self-order table-bind race, empty sessions (ut-docs#2434)

**Date:** 2026-09-19
**Card:** ut-docs#2434 — follow-up finding N3 from the ADR-0103 review
(`docs/code-reviews/2026-09-19-self-order-session-basket-2261.md`), also
flagged as a known follow-up in
`docs/code-reviews/2026-09-19-self-order-table-move-preserves-basket-2433.md`
**Complexity:** medium — Sonnet built, Opus reviewed

## What shipped

Two related bugs in `bindSelfOrderTableSession` (`internal/pages/self_order_page.go`),
both from the same root cause — the busy check and the bind were two
separate operations instead of one atomic one:

1. **Threshold gap**: the busy guard only blocked a second scan of a table
   when the existing session already had `len(Lines()) > 0`. Two phones
   scanning the same table before either guest added an item both passed
   the guard and both bound — two independent, uncoordinated baskets for
   one physical table, surfacing at checkout as two `kiosk_counter_orders`
   rows for a table the old shared-`KioskEngine` design made structurally
   impossible.
2. **Check-then-act race**: even with the threshold fixed, the busy check
   (`TableOwnerActive`) and the bind (`Create`+`SetTable`) were two
   separate `manager.mu` critical sections, so two truly-concurrent
   requests could both observe "not busy" before either bound.

Fix: `internal/pos/session_manager.go` gains
`SessionBasketManager.BindTable(tableID, tableLabel, ownToken, mover,
maxIdle, now) (token string, svc *Service, busy bool)` — a single `m.mu`
critical section that does the busy check (now with no item-count
exception — an EMPTY session holds its table exactly like a non-empty
one) and either moves the caller's own existing session onto the table or
mints a fresh one, atomically. `bindSelfOrderTableSession` now calls this
instead of the old `TableOwnerActive`-then-`Create`/`SetTable` sequence.
The B1 recency window (`selfOrderTableBusyMaxIdle`) is unchanged and
still applies to empty and non-empty sessions alike, so the "an abandoned
cart must not hold a table hostage" guarantee is unaffected by dropping
the item-count check.

ADR-0103 is corrected in place (same convention the ADR already used for
finding N4): Decision 4's "non-empty" qualifier and Decision 6's "fails
safe for this case" claim were both wrong, per this same review's finding
N3 — corrected under a `> **Correction (2026-09-19, ut-docs#2434, ...)**`
block, not a superseding ADR, since the architectural decision itself
(per-table session concurrency, a same-table-only busy guard) is
unchanged; only the guard's threshold and atomicity were wrong.

## Independent review

Opus subagent, worktree-isolated (complexity:medium → Opus review).

## Verified beyond automated tests

- TDD false-pass check done personally before Review: reverted only the
  fix hunk (`internal/pos/session_manager.go` + `internal/pages/self_order_page.go`),
  confirmed `TestSelfOrder_SameTableEmptySession_ShowsBusy` fails against
  the reverted code with the exact reported symptom (second phone's scan
  renders the normal "Tap to start" page, not the busy screen), confirmed
  the build itself fails without `BindTable` (the new
  `TestSessionBasketManager_BindTable_*` tests reference a method that
  doesn't exist on old code), restored the fix, confirmed the full
  `internal/pos`+`internal/pages` self-order suite green again under
  `-race`.
- Real concurrent race proven under `-race`, not just asserted: 32
  goroutines racing `BindTable` on the identical table id — exactly 1
  binds, the other 31 see `busy=true` with no token/service, every run.
- Mover-path atomicity covered separately
  (`TestSessionBasketManager_BindTable_MoverBlockedByBusyTableKeepsOwnBinding`):
  an existing session moving onto an already-held table stays on its
  original table with its basket untouched.
- No regression to résumé (`TestSelfOrder_SameTableRescan_NeverBusy`),
  move (`TestSelfOrder_MovingOntoBusyTable_ShowsBusyAndKeepsMoverOnOriginalTable`,
  `TestSelfOrder_SameGuestScansDifferentTable_PreservesBasket`), or
  idle-recovery (`TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard`) —
  all still pass unmodified.
- Locking discipline unchanged: `BindTable` takes `m.mu` once at its own
  top and calls `mover.SetTable` (a `Service.mu`-guarded method) while
  holding it — the same documented `manager.mu -> Service.mu` order every
  other manager method already uses; a `Service` never calls back into
  the manager, so no inversion is possible.
- `go build ./...`, `gofmt -l .` (no output), `golangci-lint run
  ./internal/pos/... ./internal/pages/...` and the whole-repo
  `golangci-lint run ./...` — 0 issues both times.
- `guard-data-access.sh` (no SQL touched), `guard-kiosk-engine.sh` (no
  `Engine` reference — only `d.SelfOrderSessions`/`KioskEngine`, unchanged
  from before), `guard-i18n.sh` (no new user-facing strings — the busy
  screen's existing `selforder.table_busy.title`-keyed copy is reused
  unchanged), `guard-page-http-error.sh` — all green.
- Full `go test ./...` (whole repo) green before commit.
- No money/tax involvement (pure session/table-binding logic, no
  `money.Money` types touched). No offline-first concern (in-memory,
  LAN-local state, no network dependency introduced or removed). No UI
  surface changed — the existing "Busy" screen template/copy is reused
  as-is, so no visual-check attestation is owed here (`tester`'s
  screenshot requirement is for a changed form/dialog/page, and none was
  changed).

## Explicitly not in scope

- ut-docs#2432 (unbounded self-order session creation / resource
  exhaustion) and ut-docs#2435 (`SessionBasketManager` mutex held across
  plugin round-trips in `SetConfig`/`HasItems`/`TableOwner`) — separate
  cards from the same ADR-0103 review, touching the same file/area but
  independent findings, not addressed here.
- Merging two same-table baskets after the fact — moot now that the race
  producing them is closed, not a mechanism this card adds.

## Safe-to-merge verdict

Yes.
