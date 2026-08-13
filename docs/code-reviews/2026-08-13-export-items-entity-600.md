# Code review: export dispatch gains a declared-entity `items` ledger

**Date:** 2026-08-13
**Branch:** `feat/600-export-items-entity`
**Closes:** universaltill/ut-docs#600 ("Export: extend the proven `export.requested.ask` plugin dispatch beyond sales/stock reports to catalog/customer entities")
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model), fresh context, isolated worktree

## Scope decision

ut-docs#600 as filed covered four catalog entities (`items`/`categories`/`tax_codes`/`customers`). Building all four in one commit risked an unreviewable diff, so this change ships the `items` entity only — it establishes the pattern (a new `Entities` declaration on export entries, mirroring the import side's ut-docs#599 pattern, plus a permission gate) that three same-shape follow-on cards now mirror: `categories` → ut-docs#654, `tax_codes` → ut-docs#655, `customers` → ut-docs#656 (the last uses `customers:read`, a distinct existing permission, plus a PII call-out). Noted on the parent issue.

## What shipped

- `internal/data.ExportEntryRow` gains `Entities []string`, unpacked from `plugin_entries.config_json` by `ListExportEntries` — exactly the shape `ImportEntryRow.Entities`/`ListImportEntries` already use (ut-docs#599).
- `internal/pages/data_api.go`'s `exportRequestPayload` gains an `Items []data.ExportRow` field. The `/api/data/export` handler populates it only when the resolved entry's `Entities` declares `"items"` **and** the plugin holds a new `items:read` permission grant — the first ledger in this payload gated on both an entity declaration and a permission, unlike the existing Sales/Stock fields (permission-gated only).
- `internal/data.CatalogRepo.ExportRows` (existing, previously CSV-only) is reused as-is for the data, not duplicated.
- `ut-docs/reference/plugin-manifest.md` documents the new field, its gating, its `[]`-vs-`null` contract, and known limitations (separate PR in `ut-docs`, `docs/600-export-items-permission-manifest`).

## Independent review

Spawned a `general-purpose` subagent, `model: opus`, isolated worktree, briefed with the exact diff scope, the #599 pattern to mirror, and told to actually run the full gate and do revert→run→restore TDD verification itself.

**Blocking, fixed before merge:**

- **F1 — `data.ExportRow` had no JSON tags, so it marshaled PascalCase** (`{"Name":"Apple","SKU":"ABC","PriceMinor":100,...}`), violating this repo's mandated snake_case wire convention and inconsistent with the adjacent `Sales`/`Stock` fields in the same payload (both purpose-built with explicit snake_case tags). Fixed: added tags matching ADR-0039's interchange-format naming (`price_minor` etc.) — safe for the existing `writeCatalogCSV` consumer, which reads struct fields directly, not via reflection/tags. The test asserting on the wire shape was locking in the violation (`json:"Name"`/`json:"SKU"`) and had to change with the fix.
- **F2 — the new permission `catalog:read` broke the `<entity>:<verb>` convention this card explicitly mirrors, and collided cross-repo.** #599's import side derives the permission mechanically from the entity (`items:write`, `customers:write`, documented in plugin-manifest.md). The export counterpart of entity `"items"` is `items:read` — `catalog:read` was a one-off that would have forced #654–656's follow-ons to special-case `items`, and it already exists in `ut-cloud` (`internal/config/config.go`) meaning "read the marketplace plugin catalog" — same string, same ecosystem, different meaning. Fixed: renamed to `items:read` throughout code, tests, and docs.

**Should-fix, addressed:**

- **F4 — empty-vs-ungranted ambiguity.** `CatalogRepo.ExportRows` returned a nil slice for an empty catalog (marshals `null`), indistinguishable from omitted — inconsistent with `StockForExport`'s deliberate `[]`-vs-`null` contract (documented in plugin-manifest.md, ut-docs#228/#240). Fixed: `ExportRows` now returns a non-nil empty slice.
- **F5 — `items[].Stock` silently disagrees with the same payload's `stock[]` for variant-tracked shops** (item-scoped `SUM(inventory.quantity)` only; variant-scoped rows are disjoint per ADR-0043 and already included in `stock[]` since ut-docs#240). Documented as a known limitation on `ExportRow` and in plugin-manifest.md rather than fixed in code — the gap is inherent to reusing the existing `CatalogRepo.ExportRows` query, which was this card's own scope decision (extending it to aggregate variant stock is a real, larger change of its own, and `stock[]` remains the source of truth for stock levels).
- **F6 — `items[]` is lossy against ADR-0039's own `items` entity definition** ("items + variants, barcodes"): one barcode per item (primary preferred), no variant rows. Same treatment as F5 — documented as a known limitation rather than expanded in scope.
- **N2/N3 (nits, cheap, added):** `TestPluginRepo_ListExportEntries_MalformedConfigDegradesGracefully` (a hand-edited/legacy `config_json` degrades to empty `Entities` rather than failing the whole listing) and `TestExportDispatch_OmitsItemsWhenOnlyOtherEntityDeclared` (an entry declaring a *different* entity, not just no entity, still omits `items` — exercises the loop body actually iterating a non-match, which matters once #654 lands a second real entity).

**Verified correct (no changes needed):** no raw SQL outside `internal/data`; nil `entry.Entities` is safe everywhere it's ranged (no panic, no "empty means everything" — confirmed by both a test and the reviewer's own revert-then-restore); all 20 pre-existing `TestExportDispatch_*` tests plus `TestExportRows`/`TestExportCSVRoundTripsThroughImporter`/`TestExportCatalogCSV_FormulaShapedValuesDefusedAndRoundTrip`/`TestCatalogCSVFormulaTriggersStaySynced` pass unmodified, proving zero behavior change for entries with no `Entities` declared and zero impact on the CSV writer; `omitempty` on `Items` correctly matches the adjacent `Sales`/`Stock` fields (neither uses it); no UI/template/`web/*` surface touched, so no docs-shots/help-topic/i18n obligation (confirmed by running those guards too).

**TDD re-verification, done by the reviewer personally** (isolated worktree, safe to mutate): neutralized only the entity-declaration gate → `TestExportDispatch_OmitsItemsWhenEntityNotDeclared` failed while the permission-only test still passed; restored, then neutralized only the permission gate → the reverse; restored again, tree clean, both pass. Confirms the two gating axes are real and independent, not one masking the other.

## Full gate (final, post-fix)

`go build ./...`, `go vet ./...` — clean. `go test ./internal/data/... ./internal/pages/... -race` (full packages, not just the new tests) — all green. `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh` — all green. A prior full-module `go test ./... -race` (36 packages, ~25 min) also ran clean against this branch before the review's fixes were layered on; the fixes are confined to the four files above and re-verified at the package level.

## Verdict

**Safe to merge.** Both blocking findings and the should-fix findings were addressed (two by code fix, two by documented limitation — a deliberate scope call, not an oversight) and independently re-verified. Nothing was deferred that changes this card's own (reduced) acceptance criteria for the `items` slice.
