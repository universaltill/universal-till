# Code review — no run-out warnings when the shop sells without tracking stock (ut-docs#4134)

**Date:** 2026-10-14 · **Branch:** `fix/4134-no-stock-warnings-when-untracked` · **Author lane:** `lane:cloud-54` (Sonnet build) · **Reviewer:** independent Opus subagent, fresh context

## What shipped

With **Settings → Stock → "Sell items without tracking stock"** (`pos.allow_negative_inventory`) on, the till no longer predicts run-outs anywhere:

- `data.POSRepo.RunningOutCount(ctx, until, shopStockUntracked)`: the one shared count behind the low-stock digest and the /reports chip. It returns 0 without querying for an untracked shop, and replaces the two duplicated loops in `alerts.runningOutCount` and the /reports handler.
- `data.SettingsRepo.ShopStockUntracked(ctx)`: the background digest reads the switch from the DB (it has no per-request state). `data.KeyAllowNegativeInventory` now owns the key and `common.KeyAllowNegativeInventory` aliases it.
- `stockLevelsForDisplay` (/inventory page and partial refresh) skips the sell-rate queries when the switch is on. Rows stay listed with their quantity and reorder-level `Low` flag. Days-left shows "—", and there is no ⚠, no order hint and no chip.
- Help: `web/help/{en,de,tr,ar,fa}/inventory.md` ("If you do not track stock at all") and `alerts.md` steps 2–3 updated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The generic `/api/settings/upsert` path uses `truthy()` (accepts `on`), while `LoadState`/`ShopStockUntracked` use `strconv.ParseBool`. A hand-typed `on` would hide predictions in the UI but still send the digest. | Pre-existing parser split; filed as ut-docs#4287 (Backlog). |
| 2 | minor | Help said "no low-stock email" but the digest is also a My shop inbox item, and `alerts.md` step 3 was unqualified. | Fixed in all five locales. |
| 3 | nit | The inventory early return duplicated the row-building loop. | Fixed: it now skips only the rate queries and keeps one loop. |
| 4 | nit | /inventory and /reports read `CurrentState()` while alerts reads the DB. | Accepted: same pattern as `pos_api.go`/`inventory_api.go`, and both write paths update state. |
| 5 | nit | German case agreement in the new inventory sentence. | Fixed. |

Clean checks: /reports with the switch off produces the same count as before (errors were already swallowed to 0). The inventory path drops only the prediction fields. No other surface predicts run-outs (back-office low-stock card and `/api/inventory/low-stock` are reorder-level based; `/api/inventory/low-stock` already honours the switch per ut-docs#27). No file I/O.

## TDD verification (re-run by the reviewer)

With the production files reverted, each new test failed with its real error:
- `TestPushDigest_UntrackedStockNeverPushes/allow_negative_inventory=true`: `pushed = true, want false`
- `TestInventoryNoPredictionsWhenStockUntracked`: `runningOut = 1, want 0`
- `TestReportsPage_RunningOutChipHiddenWhenStockUntracked`: `untracked shop must not show the running-out chip`
- `TestRunningOutCount_UntrackedShopIsZero` (flag ignored): `RunningOutCount = (1, <nil>), want (0, nil)`

All pass after restoring. Each test has a tracked-shop control.

## Gate

`gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./...` (0 issues), `go test ./internal/data/ ./internal/alerts/ ./internal/pages/...`. Every `ci.yml` guard passes locally except two that are environment-only in this container: `guard-deadcode-baseline` (local deadcode built with go1.26 against a go1.27 module) and `guard-shellcheck-version` (no shellcheck binary). CI runs both.

**Not done:** no driven browser run or screenshot. The change only omits existing elements, and the handler tests assert on the rendered HTML. Surfaces I did not look at: every theme, viewport and RTL rendering.

## Verdict

Safe to merge.
