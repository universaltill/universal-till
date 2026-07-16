# Code review — enterprise import: stock + departments (2026-07-16)

Branch: `feat/import-stock-departments`
Scope: `docs/arch/erp-inbound-import.md` — let an enterprise customer (Ansar)
load their product master **plus opening stock and department** from a file
export through the existing `/import` seam.

## Starting state

Opening-stock import was already present in the base branch: `catimport` parses
a `Stock`/`HasStock` value, and `import_page.go` records it as a `receive` stock
movement at the default location and mirrors it to inventory connectors. This
change adds the **department axis** and a dedicated **generic-erp** format, and
broadens stock/column synonym coverage.

## Changes

### `internal/catimport/catimport.go`
- `ImportItem` gains `Department string` (distinct from `Category`).
- `Result.Format` now documents `generic-erp`.
- Synonym map:
  - **department** is now its own field: `"department"`, `"dept"` — removed from
    the `category` synonym list so the two axes never collide. A file carrying
    only a category leaves `Department` empty.
  - **category** broadened: `subcategory`, `sub-category`, `class`.
  - **stock** broadened per spec: `qty`, `in_stock`, `on_hand`, `on hand`,
    `opening stock` (keeps `in stock`, `stock`, `quantity`, `current quantity`,
    `stock quantity`). Decimal quantities (weighed goods) already parse via
    `strconv.ParseFloat`.
  - **weighed**: `weighed (y/n)`.
- `DetectFormat` returns `generic-erp` when a department column is present
  (an ERP master signal), else falls through to `generic`. Loyverse/Square
  detection is unchanged and still runs first, so detection is
  backward-compatible. New helper `hasColumn` reuses the synonym table.

### `internal/data/catalog_import_repo.go` (new file — per the no-`pos_repo.go` constraint)
- `CatalogRepo.EnsureCategoryUnder(ctx, name, parentID)` — parent-scoped,
  case-insensitive find-or-create. Empty `parentID` ⇒ top-level (a department).
  Idempotent, so re-running an import resolves to the same rows. Existing
  `EnsureCategory` is left untouched.

### `internal/pages/import_page.go`
- On commit, department is resolved to a **top-level category** and the item's
  category is created/linked **under** it (departments are top-level categories,
  per `docs/arch/enterprise-department-stores.md`). Rules:
  - department + category ⇒ category nested under department, item → category.
  - department only ⇒ item → department directly.
  - category only ⇒ top-level category (prior behaviour).
- Idempotency and preview-then-import are preserved; a department failure marks
  the row `failed: department: …` mirroring the existing category path.

### Tests
- `catimport_test.go`: `genericCSV` switched to a `Category` header (keeps the
  plain-generic path); new `erpCSV` + `TestParseGenericERPStockAndDepartment`
  covers generic-erp detection, distinct dept/category axes, the `On Hand`
  synonym, decimal opening stock for weighed goods, and a department-only row.
- `catalog_import_repo_test.go` (new): `EnsureCategoryUnder` idempotency,
  case-insensitive match, parent-scoping (same category name under two
  departments ⇒ distinct rows), and top-level department has `parent_id` NULL.

## i18n

No new user-facing template strings. Row status strings (`failed: department: …`)
mirror the existing untranslated status text in the same handler.

## Verification

- `go build ./...` — clean.
- `go test ./...` — all packages pass.
- `bash scripts/ci/guard-i18n.sh` — 597 keys resolve, all locales match.
- `bash scripts/ci/guard-data-access.sh` — no inline SQL outside data/db.
- `gofmt`/`go vet` clean on touched files.

## Constraints honoured

- `internal/data/pos_repo.go` untouched; new repo method lives in
  `catalog_import_repo.go`.
- No edits under `internal/print/`, `reports_page.go`, or `eod*`.
