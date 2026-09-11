# Code review: a used stock location can now be deactivated (ut-docs#2066)

**Card:** universaltill/ut-docs#2066 — "Inventory: a used stock location can
never be deactivated — no way to clear its history"
**Complexity:** medium (Dev: inline, session model/Sonnet; Review: Opus
subagent, fresh context, independent from the Dev pass, isolated worktree)

## The bug

`POSRepo.StockLocationInUse` (`internal/data/pos_repo.go`) refused to
deactivate any stock location that had **ever** had any inventory row,
stock-movement row, or register — an `EXISTS` check with no expiry. Since
`SetStockLocationActive` only flips `stock_locations.is_active` (confirmed:
no historical query anywhere — low-stock, reports, exports, audit — filters
on that flag), this made almost any location that had actually been used
permanently un-deactivatable, and "clear the stock to zero and try again"
(the only workaround a manager could think to try) did nothing, because a
zeroed inventory row still satisfied the `EXISTS`.

## What shipped

- `internal/data/pos_repo.go`: `StockLocationInUse` now reports "in use"
  only when the location **currently** holds nonzero stock or is assigned
  to a **currently-active** register — `EXISTS (inventory WHERE
  location_id = ? AND quantity <> 0) OR EXISTS (registers WHERE
  location_id = ? AND is_active = 1)`. `stock_movements` dropped from the
  check entirely (pure append-only audit trail, never filtered by a
  location's active state).
- `internal/pages/locations_page.go`: doc comment updated to match.
- `web/locales/{en,ar,fa,tr}.json`: reworded the existing
  `locations.error.in_use` key ("...with existing inventory, stock
  movements, or a register" → "...that still holds stock or has an active
  register") — no new keys added, so no lang-pack-drift follow-up is
  needed for `ut-plugin-language-{de,es}`.
- `web/help/en/inventory.md`: the "Stock locations" section's deactivate
  step no longer describes the old "no way to clear that history away"
  dead end; states the actual (now-working) retirement path. `ar/de/fa/tr`
  deliberately left untouched — their `inventory.md` is already tracked as
  known drift (ut-docs#330/#341, confirmed still green via
  `guard-help-drift.sh` before and after this change) and never described
  this restriction in the first place, so there was nothing there to go
  stale.
- `web/help/img/manifest.json`: regenerated via `make docs-shots`
  (pre-installed Chromium). Only the `inventory`/`en` topic hash changed
  (the markdown edit); the screenshot's own pixels are byte-identical
  (nothing about `/inventory`'s rendering changed). Four unrelated
  screenshots (`catalog` en/fa/tr, `sell` en) came back with incidental
  anti-aliasing-only diffs from the same `make docs-shots` run — reverted,
  same convention as prior sweeps in this repo.
- Tests: `internal/data/pos_repo_stock_location_test.go` (rewrote
  `TestStockLocationInUse` for the new semantics: nonzero→in-use,
  zeroed→not; active register→in-use, retired register→not; movement-only
  history→not in-use, where it used to assert the opposite for all three
  changed cases) and `internal/pages/locations_page_test.go` (new
  `TestLocationsPage_DeactivatableOnceStockCleared`, driving the actual
  reported repro end-to-end through the handler: refused while stock is
  nonzero, succeeds once cleared, history rows survive).

## Independent review (Opus, fresh context, isolated worktree)

Verified the premise the fix rests on by grepping the whole codebase
rather than trusting the design note: `RegisterLocationID` is the *only*
query anywhere that filters on `stock_locations.is_active`; every
historical join (`GetLowStockItems`, `ListStockLevels`, `export_repo.go`,
`fiscal_repo.go`) is deliberately unfiltered, so nothing is orphaned by
deactivation. Confirmed `StockLocationInUse` has exactly one caller.
Confirmed the schema backs the SQL (`registers.is_active` `NOT NULL
DEFAULT 1`, no NULL hole; `inventory.quantity` `NOT NULL`).

**One real defect found and fixed in review, not caught by the Dev pass's
own verification:** the original `quantity <> 0` exact-float compare
re-creates the bug in miniature. `RecordStockMovement` accumulates
`quantity = quantity + ?` on a `REAL` column; reviewer reproduced through
the real code path (receive 0.1, receive 0.2, adjust −0.3) landing on
`5.551115123125783e-17` — displays as `0.00` to a manager, but still trips
the exact `<> 0` check, silently reproducing the exact dead end this card
exists to fix, just rarer. Fixed to `ABS(quantity) > 0.000000001`, the same
epsilon `refund_page.go` already uses for quantity comparisons. New
regression test `TestStockLocationInUse_FractionalStockClearedToZero`,
driven through `RecordStockMovement` (not a hand-inserted row) so it
actually exercises the arithmetic that produces the residue.

**Two findings deliberately not fixed here — flagged for follow-up:**

1. Retiring a register on a location, deactivating that location, then
   *reactivating* the register silently relocates its future sales to
   Main (`ResolveStockLocationID`'s existing fallback) without any warning
   at the point of reactivation — a sequence the old, over-broad guard
   made unreachable. Largely mitigated: this fallback is pre-existing,
   intentional behavior (`registers_page.go`, and already documented in
   `inventory.md`'s "How a sale and a refund change stock" section), just
   newly reachable via a different route. Worth a small follow-up card
   (warn on reactivating a register whose location is inactive) rather
   than expanding this diff's scope.
2. `StockLocationInUse` then `SetStockLocationActive` remain two separate
   statements (check-then-act, not transactional) — pre-existing, benign
   here since the guarded paths can't target a location mid-deactivation
   in a way that matters, and unchanged by this diff either way.

Also checked and clean: no `os.MkdirAll`/`paths.Data` surface (no
file-write code in this diff at all); no real client/shop name or
secret-shaped literal; all four locale files changed the same key,
structurally parallel, and the Arabic reword actually *fixes* a prior
inconsistency (`سجل نقدي` → `صندوق`, now matching
`registers.error.last_active`'s own established term) — no mistranslation
signal in `fa`/`tr` either.

## Independent TDD re-verification

Reviewer reverted `pos_repo.go` to the pre-fix query on a worktree copy and
ran the new/changed assertions individually: all four failed red with
clear messages (nonzero-cleared-to-zero, retired-register, movement-only
history — the last two via a temporary isolated test file, deleted after,
tree confirmed clean). Restored → all four green. Separately TDD'd the
epsilon fix itself: reverted to the exact `<> 0` compare, new fractional
test failed with the residue value in the message; restored, passed.

## Verified beyond automated tests

- `gofmt -l` clean on every changed file; `go build ./...`, `go vet ./...`
  clean.
- `go test ./internal/...` — full suite green (both before and after
  pulling the review's epsilon fix back in).
- `golangci-lint run ./internal/data/... ./internal/pages/...` — 0 issues.
- `bash scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-docs-shots.sh` — all green on the final tree.
- `guard-deadcode-baseline.sh` could not run in this sandbox (needs the
  desktop-webview cgo build's `gtk+-3.0`/`webkit2gtk-4.1`, not installed
  here) — not a code finding; this diff adds no new unreferenced code.

---
_Generated by [Claude Code](https://claude.ai/code)_
