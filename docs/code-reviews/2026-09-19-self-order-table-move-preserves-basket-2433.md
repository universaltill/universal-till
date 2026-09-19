# Code review: self-order table move preserves the guest's basket (ut-docs#2433)

**Date:** 2026-09-19
**Card:** ut-docs#2433 — follow-up finding N2 from the ADR-0103 review
(`docs/code-reviews/2026-09-19-self-order-session-basket-2261.md`)
**Complexity:** medium — Sonnet built, Opus reviewed

## What shipped

`internal/pages/self_order_page.go`'s `bindSelfOrderTableSession`: when a
guest's browser already holds a live self-order session bound to table A
(with items in the basket) and scans a DIFFERENT table B's QR — they moved
seats — the handler used to unconditionally remove that session and mint a
fresh, empty one for table B, silently discarding whatever the guest had
already added. There was no warning and no test coverage for this path (the
ADR-0103 review's own `-coverprofile` run found it as the one zero-coverage
line the whole original feature diff added).

Fix: instead of removing and re-minting, the handler now rebinds the
**same** session in place via `current.SetTable(t.ID, t.Label)` — the exact
mechanism (ADR-0054/ut-docs#820) the cashier's own table picker already uses
to move a live sale between tables. The session token/cookie is unchanged;
the basket carries over because it's the same `*pos.Service`, just
repointed to the new table id/label. The old table is freed automatically
— `TableID()` now reports the new table, so `TableOwnerActive` no longer
finds this session there.

Chosen option (a) from the card, matching the cashier precedent, not a
confirmation-dialog UI (option (b)) — no new UI, no new copy.

## Independent review

Opus subagent, worktree-isolated (complexity:medium → Opus review).

### Should-fix — both addressed

- **S1**: the function's doc comment claimed `SetTable` "always takes
  effect" for a reason (fresh, empty, dine-in-default basket) that only
  applies to the mint path, not the new move-path call on a live,
  non-empty basket. The move path only stays safe because a *different*,
  non-local invariant holds today: `self_order_shop.go` clamps a
  table-bound session's order type to never become Takeaway
  (`TestSelfOrderShop_TableCheckout_TakeawayToggleCannotUnbindTable`), so
  `hasDineInLine` is always true here too — but nothing at the new call
  site said so, and if that clamp is ever relaxed, `SetTable` would
  silently no-op and strand the moved guest's basket with no table.
  **Fixed**: doc comment rewritten to name the actual dependency and the
  silent-failure mode if it's ever removed.
- **S2**: no test covered a guest moving onto a table that's already busy
  (held by a different, non-empty, recently-active session) — the one
  place the move interacts with ADR-0103 Decision 4's busy guard.
  **Fixed**: added
  `TestSelfOrder_MovingOntoBusyTable_ShowsBusyAndKeepsMoverOnOriginalTable`
  — busy screen shown, mover's own session/table/basket untouched,
  incumbent undisturbed, no session leaked.

### Nits — addressed

- **N1**: the original regression test never asserted `TableLabel()`, only
  `TableID()` — a transposed/wrong second argument to `SetTable` would have
  passed silently, and the label is what the guest sees in the cart and
  what's written to `kiosk_counter_orders.table_label` for the pay-at-
  counter board and kitchen ticket. Added the assertion.
- N2 (commit message) / N3 (this record): addressed by this commit.
- N4 (help-manual mention of table-move behavior): left as-is — the
  reviewer confirmed no rule is violated (`guard-help-topics.sh` green, and
  the old silent-discard behavior was never documented either, so nothing
  is now stale), and it's a nice-to-have, not a defect. Not filed as a
  separate card — small enough to fold into any future self-order manual
  pass.

### Confirmed correct (independently verified by the reviewer)

- **TDD re-verification**: reverted only the fix hunk, confirmed the new
  regression test fails with the exact symptom described (`basket has 0
  lines after moving tables, want 1`), confirmed the *rest* of the
  `TestSelfOrder*` suite still passes against the reverted code (the
  ADR-0103 suite was entirely blind to this bug), restored the fix,
  confirmed green again.
- No nil-deref risk: `current` is only non-nil when `currentToken != ""`,
  which only happens on a live `Get` hit.
- No double-ownership or false-free window: `SetTable` mutates `tableID`
  under `Service.mu`; `TableOwnerActive` reads it under the same mutex
  while holding `manager.mu` — a session has exactly one `TableID()` at any
  instant.
- Locking discipline unchanged: the diff takes only `Service.mu`, matching
  the package's documented `manager.mu -> Service.mu` order. Race detector
  clean under a concurrent-moves stress scenario (2× full self-order suite
  plus 50 concurrent same-cookie moves alternating between two tables, with
  concurrent `TableOwnerActive`/`Len`/`HasItems` scans) — reproduced again
  in this session via `go test ./internal/pages/... ./internal/pos/...
  -race`.
- The residual two-sessions-on-one-table race (both guests pass the busy
  check before either has items) is real but pre-existing and already
  filed as ut-docs#2434 (finding N3 of the same ADR-0103 review) — this fix
  does not widen it.
- End-to-end correctness: `completeCounterOrderCheckout` reads
  `TableID`/`TableLabel` straight off the moved session, so the counter
  order and kitchen ticket land on the guest's new table.
- CLAUDE.md compliance, all guards re-run green: `guard-data-access.sh` (no
  SQL touched), `guard-kiosk-engine.sh` (no `Engine` reference), `guard-i18n.sh`
  (no new user-facing strings — the test's busy-screen literal is the
  existing `selforder.table_busy.title` key's rendered value), `guard-help-topics.sh`,
  `guard-compliance-claims.sh`. No money/tax, no network dependency
  (pure in-memory state, offline-first unaffected).

## Verified beyond automated tests

- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` (no output).
- `go test ./internal/pages/... ./internal/pos/...` and `... -race`, all
  green.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- Full `go test ./...` (whole repo) green before review.
- Every relevant CI-blocking guard re-run locally green (listed above).

## Explicitly not in scope

- Option (b) from the card (a UI confirmation before discarding) — not
  chosen; (a) matches existing precedent and does the least surprising
  thing, per the card's own recommendation.
- ut-docs#2432 (unbounded session creation) and ut-docs#2434 (two-phones
  race on an empty table) — separate cards from the same ADR-0103 review,
  not touched here.

## Safe-to-merge verdict

Yes.
