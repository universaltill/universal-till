# Review: Inventory lists every tracked item; "+", override and return panels removed (ut-docs#3631)

Date: 2026-10-08 · Lane: lane:cloud-41 · Built by: Claude Opus 5.5 (Dev subagent) · Reviewed by: Claude Fable 5.1 (independent subagent, separate worktree)

## What shipped

Product-owner bugs on the till's `/inventory` page:

- **Every active, stock-tracked item is now listed.** New method `POSRepo.ListStockLevelsIncludingUnstocked` returns `ListStockLevels`' rows plus zero-quantity rows at the shop's active **Main** location. That covers each active, tracked item with no inventory row and no active variants, and each active variant of such an item with no row of its own. The result is sorted case-insensitively by item, then variant, then location. Only `stockLevelsForDisplay` (the page and its `stock-updated` partial) calls it. `ListStockLevels` itself is unchanged, so export, cloudsync, alerts, reports and core views keep today's payload. Variant rows stay display-only (ut-docs#2082).
- **Removed: the "+" add-stock button**, its click listener and the blank-open path. Tapping a row is now the way in.
- **Removed: the "Manager override — negative stock" panel.**
  - Removed with it: `POST /api/inventory/override`, `CreateNegativeInventoryOverride`, `pos.RecordNegativeInventoryOverride`, `POSRepo.RecordNegativeInventoryOverride` and its type.
  - It only wrote an `audit_log` row that nothing read. The adjust dialog already records signed corrections as `stock_movements` with their actor, behind the same `stock_management` gate. Cashiers can't open `/inventory` (#3079), so the PIN path loses nothing.
- **Removed: the "Process a return" panel.**
  - Removed with it: `POST /api/inventory/return`, `GET /api/inventory/return/lines`, `CreateReturn`, `GetReturnLines` and their helpers and types.
  - Returns stay on Journal → `/refund` and the sell screen. Neither is touched.
- Also removed:
  - the demo-mode allow-list entries;
  - 15 `inventory.*` locale keys from en/ar/fa/tr (language packs de/es/pt: their own PRs, same cycle);
  - the two help sections in `web/help/*/inventory.md`, with steps 1–2 rewritten and translated, plus the inventory-return mention in `sell.md`;
  - comments pointing at the removed endpoints.
- Help-drift baseline: the English heading count is now 8. Docs-shots are regenerated (only the 4 inventory PNGs and the manifest changed).
- ut-docs `reference/contracts/fiscal-sign-ask.md` and `guides/pos.md` are updated in a companion PR.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The variant branch's correlated `NOT EXISTS (… inv.variant_id = v.id)` scanned `inventory` once per variant. The only index is `(item_id, variant_id, location_id)` and variant rows have `item_id` NULL. `EXPLAIN QUERY PLAN` showed a SCAN, on every page load and every partial refresh. | **Fixed:** rewritten as `v.id NOT IN (SELECT variant_id FROM inventory WHERE variant_id IS NOT NULL)`, so the set is built once. Semantics are unchanged and the tests still pass. |
| 2 | nit | A zero row with no active Main has `data-location=""`. The dialog's context line then read "Item — " with a dangling dash. | **Fixed:** the line names the location the select will actually target. |
| 3 | nit | `reports.md` still lists "stock overrides" among audit-log events. | Accepted: historical rows still exist in shops' audit logs. |
| 4 | nit | golangci-lint can't run in the cloud container (binary built with go1.25, repo on go1.27.1). | Substitute: staticcheck U1000 (the same `unused` analyzer) is clean, and the reviewer scanned for unreferenced unexported funcs and found none. CI is the backstop. |
| 5 | nit | e2e titles skip "(b)" after the add-button test was dropped. | Accepted (cosmetic). |
| 6 | note | `/inventory` row order is now case-insensitive (Go sort) instead of SQLite BINARY. | Accepted: it's an improvement, and only this page is affected. |

Reviewer verified:
- **No leftovers:** a repo-wide grep for the removed ids, routes and functions finds none. `#offline-flag` on `/refund` and the sell screen is untouched, and `app.js` null-guards its absence.
- **Locales:** the four files are valid, have no duplicate keys and match.
- **No refund coverage lost:** every dropped `CreateReturn` behaviour test has a `/refund` twin (fiscal gate ×6, cannot-sign, sign-ask/start). The only test without a twin covered the deleted code's own rounding.
- **SQL semantics:** the schema's CHECK keeps `item_id`/`variant_id` exclusive, so the probes are exact.

## Verified beyond unit tests

- **TDD re-verified by the reviewer:** with the method stubbed to `ListStockLevels`, 5 of 6 new data tests fail. The sixth, the no-duplicate test, is a non-regression guard. With the code restored, all 6 pass.
- **Gate:** `gofmt`, `go build`, `go vet`, full `go test ./...`, and every build-job guard (i18n, data-access, help-topics, help-drift, docs-shots, no-showmodal, kiosk-engine, core-neutral, compliance, and others) pass. shellcheck isn't installed here, and no `.sh` file changed.
- **e2e:** the 8 affected specs pass (31 tests, Playwright, pre-installed Chromium). After the final fixes, `inventory-stock-dialog-2011` and `inventory-to-till` were re-run: 8 passed.
- **Looked at:** the regenerated `inventory.png` in en (LTR) and fa (RTL) at 1024×600. The list is full width, with no "+" button and no side panels, and nothing is clipped. The dialog at 1024×600 and 360px is covered by e2e (e). **Not looked at:** the page at 360px and the dark theme. The page markup only lost elements, with no new CSS.

## Verdict

Safe to merge.

## Deferred

- None new. The variant-adjust limitation stays as described in ut-docs#2082.
