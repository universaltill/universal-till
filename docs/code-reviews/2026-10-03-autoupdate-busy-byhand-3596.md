# Code review — autoUpdateBusy ignored add-by-hand-only baskets (ut-docs#3596)

- **Date:** 2026-10-03
- **Branch:** `fix/3596-autoupdate-busy-byhand` (on `main` 2cd3f2c)
- **Card:** universaltill/ut-docs#3596. Found by the independent review of
  ut-docs#3586 (`docs/code-reviews/2026-10-03-resume-overwrites-byhand-order.md`),
  which fixed the same `HasItems()`/`ItemCount()` blind spot for
  `resumeHeldSale` and `POST /api/pos/hold`.
- **Author:** Sonnet 5 (session model, inline). **Reviewer:** Opus 5.5,
  fresh context, independent second opinion.

## What shipped

- `internal/pages/update_follow.go`: `autoUpdateBusy` now calls
  `d.Engine.HasItemsOrByHand()` / `d.KioskEngine.HasItemsOrByHand()`
  instead of `Basket().ItemCount() > 0`. `d.SelfOrderSessions.HasItems()`
  is unchanged. Comment updated to explain why (ut-docs#3596/#3586).
- `internal/pages/update_follow_test.go`: new test
  `TestFollowTick_RestartGateReadsByHandOnlyBasketAsBusy`, modelled on the
  existing `TestFollowTick_RestartGateReadsTheLiveBasket` and on
  `hold_api_test.go`'s `TestResumeHandler_AutoParksByHandOnlyBasketThenResumes`
  — builds a by-hand-only live basket via `RestoreHeld` and asserts the
  restart gate reports busy.
- `internal/pos/session_manager.go`: one-line comment fix on
  `SessionBasketManager.HasItems` — it referenced the now-removed
  `d.KioskEngine.Basket().ItemCount() > 0` check; updated to name
  `HasItemsOrByHand()` and ut-docs#3596.

No new SQL, i18n keys, user-facing strings, money arithmetic, file writes,
plugin or signing code, schema changes, or UI surface. No secrets, no real
shop/client names.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | — | Locking/nil-safety: `HasItemsOrByHand()` and `Basket()` both take `s.mu` the same way; neither guards a nil receiver, same as before. No regression. | Confirmed, no change needed. |
| 2 | — | All four callers of `autoUpdateBusy` (`update_follow.go:174,189`, `update_api.go:224`, `shop_type_layout.go:62`) pick up the fix automatically. | Confirmed. |
| 3 | low | `anyBasketHasItems` (`sync_assets_prune.go:98`) still uses `HasItems()`, flagged by the reviewer as a possible same-shape gap. | **Not a bug**, on closer trace: the #3586 review already ruled this correct — `ByHandLine` carries no synced-asset reference, so a by-hand-only basket has nothing for the pruner to protect. A Backlog card (ut-docs#3607) filed on this and the next finding was closed not-planned after verifying both. |
| 4 | low | `SessionBasketManager.HasItems` (`internal/pos/session_manager.go`) checks priced lines only; reviewer flagged as possibly reachable via a table-QR session. | **Not reachable**: traced every call site of `RestoreHeld` (the only method that ever sets `BasketSnapshot.AddByHand`) — the sole production caller (`hold_api.go:439`) always targets `d.Engine`, never a session's own `sb.svc`. Comment fixed (see above) since it referenced the old check by name; no behaviour change needed. |
| 5 | nit (test) | New test doesn't assert the idle baseline (empty basket) before adding the by-hand line. | **Accepted**: the neighbouring `TestFollowTick_RestartGateReadsTheLiveBasket` already covers that baseline in the same file. |

## Verification beyond the automated gate

- TDD re-verified twice: once inline by Dev (revert fix → test fails with
  the expected message → restore → test passes), once independently by
  the reviewer subagent in a fresh run — both show the new test failing
  without the fix and passing with it.
- `gofmt -l`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./internal/pages/...`: clean.
- `go test ./internal/pos/... -race`: clean (this is the package owning
  the actual lock/field logic exercised by the fix).
- `go test ./internal/pages/... -race -run
  'TestFollow|TestHold|TestResume|TestAutoUpdate|TestStartAutoUpdateScheduler'`:
  clean (28.7s).
- `go test ./internal/pages/... -race` (the full package, unscoped): hit
  Go's test-binary timeout twice (10m default, then an explicit 25m) in
  this sandbox — not from this diff. The goroutine dump both times shows
  the hang in a pre-existing, unrelated, heavy test
  (`TestImport_BkpOversizedImageWarnsAndFallsBackToPlaceholder`, PNG
  encoding under race instrumentation), not in anything this change
  touches. CI runs the full suite with its own timeout budget as the
  authoritative gate.

## Verdict

Safe to merge. No blockers.
