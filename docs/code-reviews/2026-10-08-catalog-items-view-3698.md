# Review: `catalog.items.v1` core read view (ut-docs#3698)

**Date:** 2026-10-08 · **Lane:** lane:cloud-24 · **Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent)

## What shipped
- `universal-till`: a new core read view, `catalog.items.v1` (`view:inventory`), registered in `internal/data/core_views.go`. It is backed by `CatalogRepo.ListCatalogViewItems` (`internal/data/catalog_view_repo.go`).
  - Each row has: `id`, `sku`, `name`, `category_id`, `category`, `price_minor`, `unit`, `weighed`, `active`.
  - It lists every item, including inactive and sample items. Order is name (SQLite `NOCASE`), then id.
  - Paging arguments: `offset` 0–1,000,000 and `limit` 1–500 (default 500).
- `ut-docs`: `reference/contracts/plugin-views.md` goes to 1.1.0 with the view's section. `reference/plugin-host-functions.md` lists the view, and ADR-0149 §6 notes the extra fields.
- Scope: #3698 was cut down to this view. `users.list.v1` and `sales.receipts.v1` moved to #3976. `sku` was added beyond ADR-0149's field list, because camera identify needs it for `add_to_basket` when the AI engine moves into its plugin (#2851).

## Tests (TDD)
- `TestCatalogItemsView` checks the exact JSON, NULL handling, the weighed item, ordering, paging with no overlap, and that past the end returns `[]`.
- `TestCatalogItemsViewArgs` checks the argument bounds.
- `TestCoreViewRegistry_FirstSet` now expects 6 views. The row shape is pinned in `TestCoreViewRowShapesArePinned`.
- `TestViewQueryCatalogItems` (internal/plugins) runs through a real wasip1 guest: a granted query returns the row, and a query without `view:inventory` returns `-2` and is audited.
- **TDD re-verified by the reviewer.** With the registration removed, all three data tests fail ("not registered", `unknown argument "offset"`, "5 views registered, want the 6"). With it restored, they pass.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No unit in the row, so a weighed item's per-kg price looks like a per-each price to a plugin | **Fixed**: `unit` and `weighed` added, tested and documented |
| 2 | minor | The doc said "case-insensitive", but `NOCASE` folds ASCII only | **Fixed**: the doc says ASCII case-insensitive |
| 3 | nit | Sample items are listed silently | **Fixed**: the contract says so |
| 4 | nit | The code comment claimed 500 rows are "well below" 256 KiB, but names have no length cap | **Fixed**: the comment gives the ~300-byte average-name threshold. The doc already covers `-5` |
| 5 | nit | Offset paging is not a snapshot | **Fixed**: the doc says so |
| 6 | nit | No index serves the `NOCASE` sort | Accepted: fine at till scale, well inside the 5 s deadline |

The reviewer checked and found these clean:
- SQL NULL handling.
- Category soft delete is consistent with the other readers.
- No hard-coded list of view names anywhere else; the manifest gate validates the name pattern only.
- Money: `int64` minor units at the view boundary, matching the neighbouring view structs.
- No PII or card data.
- No file writes.

## Gate
- Passed: `gofmt -l` (empty), `go build ./...`, `go test ./...` (full, then `./internal/...` again after the fixes).
- Guards passed: `guard-data-access`, `guard-core-neutral`, `guard-readme-local-links`, `guard-sdk-hostfns`, `guard-i18n`, `guard-card-data-schema`.
- Not run locally: `golangci-lint` and `guard-deadcode-baseline`. This container's binaries were built with Go 1.25/1.26 and the repo targets 1.27, so CI runs them.

## Verdict
Safe to merge. It is a read-only addition with no UI and no migration, behind an existing permission.
