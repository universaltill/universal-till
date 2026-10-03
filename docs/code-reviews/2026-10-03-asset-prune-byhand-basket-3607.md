# Code review — replica asset pruner misses by-hand-only baskets (ut-docs#3607)

- **Date:** 2026-10-03
- **Branch:** `fix/3607-asset-prune-byhand-basket`
- **Author:** Sonnet 5 (pipeline lane `lane:cloud-41`)
- **Reviewer:** independent Opus 5.5 subagent (different model from the author), one round

## What shipped

ut-docs#3596's review (still in progress elsewhere) found two more spots
with the same gap it fixed: `HasItems()` (priced lines only) instead of
`HasItemsOrByHand()` (ut-docs#3586), so a resumed kiosk pay-at-counter order
whose lines all failed to match the catalog — and so holds only `AddByHand`
entries — reads as an empty basket.

- `internal/pages/sync_assets_prune.go`'s `anyBasketHasItems` (gates the
  replica asset pruner so it never deletes a photo mid-sale): `d.Engine` and
  `d.KioskEngine` now check `.HasItemsOrByHand()`. `d.SelfOrderSessions.HasItems()`
  is left unchanged.
- `internal/pos/session_manager.go`'s `SessionBasketManager.HasItems()`
  (the method behind that third disjunct): confirmed unreachable for
  by-hand lines today — `AddByHand` is populated on a `*pos.Service` only
  inside `RestoreHeld`, whose only call site (`hold_api.go:488`) always
  targets `d.Engine`, never a table-QR session's own `*Service`. Left as
  priced-lines-only, with a comment recording the finding and a revisit
  condition.
- `scripts/ci/deadcode-baseline.txt`: `Service.HasItems` (`internal/pos/hold.go`)
  added — no longer called from production code after this fix, but kept
  (not deleted) because 34 test call sites across the repo test its exact
  priced-lines-only contract directly, matching the guard's own documented
  test-only-reachable convention (e.g. `ResetCacheForTests`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| — | — | Reviewer independently re-derived the site-2 unreachability claim (traced every `AddByHand` write, every `RestoreHeld` call site, the session factory and `BindTable`'s `mover` path) and confirms no path aliases a session's `Service` with `d.Engine`, and none restores a held sale into one. | Confirmed, no change. |
| nit | nit | The doc paragraph just above the new comment on `SessionBasketManager.HasItems` (predates this diff) still calls it the "twin of `d.KioskEngine.Basket().ItemCount() > 0`" — `update_follow.go` no longer reads that way once #3596 lands. | Accepted — pre-existing text, not introduced here; worth tidying once #3596 merges, not blocking this card. |
| nit | nit | No new test covers a priced-lines-only basket read as busy (only by-hand-only and empty). | Accepted — already covered indirectly by the existing `TestPrune_OpenSaleDefers`. |

Reviewer also checked and found OK: no race (`HasItemsOrByHand` takes the
same `s.mu` `HasItems` did), scope stays to the two call sites named in the
card (4 files touched total), `deadcode-baseline.txt` entry is on the
correct sorted line and matches the guard's own tool output, and the two
pre-existing local-only guard findings (`logging.Stderr`,
`timestampWriter.Write`) are a known GTK-headers-missing local limitation
(ut-docs#2425) present on `main` before this branch, reproduced via `git
stash` — not caused by this diff.

## Verification

- TDD: reviewer independently reproduced the new tests failing against the
  pre-fix code in a separate worktree (`TestAnyBasketHasItems_ByHandOnly*`),
  then passing after the fix.
- `go build ./...`, `go vet ./...` clean; `go test ./internal/pages/...
  ./internal/pages/... -race` and `./internal/pos/...` green (author and
  reviewer, independently).
- `guard-data-access.sh`, `guard-i18n.sh` clean (no SQL, no new strings).
- `guard-deadcode-baseline.sh`: clean except the two pre-existing,
  unrelated local-only findings above.
- No UI surface, no money, no new host/plugin event, no new user-facing
  string — pure internal correctness fix. Accepted coverage gap, stated
  out loud: no Playwright e2e run — the change is an internal busy-check
  for an async replica pruner with no HTTP-visible behaviour change, and a
  browser-driven run would not exercise it; existing Go test coverage
  (including a reproduction of the original bug) is proportionate.

## Verdict

Safe to merge.
