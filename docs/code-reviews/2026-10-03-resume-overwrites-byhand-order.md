# Code review — resuming over a by-hand-only order overwrote it (ut-docs#3586)

- **Date:** 2026-10-03
- **Branch:** `fix/3586-resume-overwrites-byhand-order` (on `main` 1842c4e)
- **Card:** universaltill/ut-docs#3586. It was found in the review of
  ut-docs#3423 (universal-till#1675, still open), which fixed the same
  `HasItems()` blind spot in `POST /api/pos/reset`'s discard audit. This fix
  does not depend on that PR.
- **Author:** Sonnet 5 (Dev subagent). **Reviewer:** Opus 5.5, fresh
  context, independent second opinion.

## What shipped

- `internal/pos/hold.go`: new method `Service.HasItemsOrByHand() bool`. It
  returns `len(s.lines) > 0 || len(s.basket.AddByHand) > 0` under `s.mu`.
  `HasItems()` is unchanged; only its doc comment now says "priced lines".
- `internal/pages/hold_api.go`: two checks now call `HasItemsOrByHand()`
  instead of `HasItems()`.
  - `resumeHeldSale`'s busy check. A live pay-at-counter order with only
    "add by hand" lines is now auto-parked before another order is
    restored. Before the fix, `RestoreHeld` replaced it and it was lost:
    no held row, no sale and no audit entry. The resume had already claimed
    and deleted its `held_sales` row. This check covers both
    `POST /api/pos/resume` and the `/open-orders` resume route, which share
    `resumeHeldSale`.
  - `POST /api/pos/hold`'s empty-basket refusal. Hold now parks such an
    order instead of showing `hold.error.empty`.
- `internal/pages/hold_api_test.go`: two new tests.
  - `TestResumeHandler_AutoParksByHandOnlyBasketThenResumes`
  - `TestHoldHandler_ByHandOnlyBasketIsNotRejectedAsEmpty`
- `CHANGELOG.md`: Fixed entry, added by the reviewer.

No new SQL, i18n keys, user-facing strings, money arithmetic, file writes,
plugin or signing code, or schema changes. The diff has no secrets and no
real shop or client names. `web/help/en/open-orders.md` step 2 already says
"If you already have a sale going, it is held for you first". The fix makes
the code match that text, so no help or reference update is needed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | **Same blind spot in `autoUpdateBusy` (`internal/pages/update_follow.go:160`).** The unattended-update restart gate uses `d.Engine.Basket().ItemCount()`, which counts only priced `Lines`. The live basket is in memory only, and the order's `held_sales` row was claimed and deleted when it was resumed. So a replica that auto-updates while a by-hand-only counter order is open restarts and loses that order. This is the same data-loss class on a different code path, outside this card's two call sites. | **Out of scope**: new Backlog card. The fix is one line: `d.Engine.HasItemsOrByHand()` in `autoUpdateBusy`, plus a test. |
| 2 | low | **Dismissing the notice makes the order look empty again.** After `POST /api/pos/add-by-hand/dismiss` on a by-hand-only order, the basket has no lines and no `AddByHand`. Resuming another order then replaces it without parking it. The order's identity (`DisplayNo`, `HeldOrigin`) is gone with no audit entry. The cashier explicitly dismissed the only content, so there is nothing to sell, but the order number vanishes silently. | **Out of scope / accepted**: needs a product decision (does a dismissed, empty counter order still count as an order?). Can be noted on the #3586 follow-up card. |
| 3 | low (cosmetic, pre-existing) | A parked by-hand-only order shows `LineCount` 0 and total 0 on the held strip and Open orders. `parkCurrentBasket` writes `LineCount: len(snap.Lines)`, the same value `open_orders_counter.go` writes when it converts the order. The by-hand lines still travel in the payload, and the test checks this. | **Accepted**: pre-existing and unchanged by this diff. |
| 4 | — | `anyBasketHasItems` (`sync_assets_prune.go:98`) still uses `HasItems()`. | **Not a bug**: that check stops asset pruning while a sale might use synced assets such as images. `ByHandLine` has only name, qty and modifier names and uses no synced asset, so leaving it unchanged is correct. `KioskEngine` and `SelfOrderSessions` never get `AddByHand`: only `d.Engine` is ever `RestoreHeld`'d. |
| 5 | nit (test) | Before the fix, the resume test fails first at the parked-and-resumed toast check (line 344), not at the held-row checks. The held-row checks (`count == 1`, id `hold-byhand-1`, payload still has `"name":"Latte"`) would also fail before the fix (no rows remain), so the test is not a tautology. | **Accepted** |
| 6 | nit | No CHANGELOG entry. | **Fixed** (reviewer added a Fixed entry). |

### Locking and correctness of `HasItemsOrByHand`

- It takes `s.mu` once and reads two fields. It calls no other method, so
  there is no re-entry into the non-reentrant mutex. `-race` on the new
  tests is clean.
- The busy check and the later `Snapshot()` in `parkCurrentBasket` are two
  separate locks. That check-then-act gap existed before with `HasItems()`
  and is not made worse.

### `parkCurrentBasket` with a by-hand-only snapshot

- `Snapshot()` copies `AddByHand` and `DisplayNo` into the payload, and the
  test confirms the line survives.
- `LineCount` is 0 and `TotalMinor` is 0 (finding 3).
- `HeldOrigin` is set from the earlier resume, so the order is re-parked
  under its own id, label and `created_at`. The `Claimed` flag is passed
  through to `heldSaleWriteThrough`.
- `fiscal.order.start` is not fired again. It runs only on a first park.
- Any table claim is kept, as for every park.
- `d.Engine.Reset()` then clears the basket, so the next `RestoreHeld` starts
  from an empty basket.

## Verified by the reviewer

- **TDD, re-verified.** The reviewer reverted only `internal/pos/hold.go`
  and `internal/pages/hold_api.go` to `main` and ran the two new tests.
  - Both failed:
    - Resume test: no parked-and-resumed toast. The order was replaced, not
      parked.
    - Hold test: `hold.error.empty` was shown.
  - With the fix restored, both pass, including under `-race`.
- **Gate (run by the reviewer):**
  - `go build ./...`, `go vet ./...`: OK.
  - `gofmt -l` on the changed files: clean.
  - `golangci-lint run internal/pages/... internal/pos/...`: 0 issues.
  - `go test ./internal/pages/... ./internal/pos/... -count=1`: all OK.
  - Guards: `guard-data-access`, `guard-i18n`, `guard-competitor-naming`,
    `guard-compliance-claims`, `guard-core-neutral`, `guard-kiosk-engine`
    and `guard-help-drift` pass.
- **Every `HasItems()` caller was checked** (findings 1 and 4).
  `SessionBasketManager.HasItems` is a separate method and never sees
  by-hand lines.
- **No visual surface changed.** Only the existing toasts and basket render
  are used, so there are no screenshots and no driven run.

## Verdict

Safe to merge. Finding 1 (`autoUpdateBusy`) should be filed as a Backlog
card. It is the same bug class with real data-loss potential, but it is
outside this card's scope.
