# Code review — CatalogRepo.GetVariantLabel/GetItemLabel leak a retire-mangled sku (ut-docs#2355)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2355 (`complexity:easy`)
- **Branch:** `fix/2355-variant-label-retire-mangle-leak`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — a clean instance that never saw the
  dev reasoning).
- **Verdict: needs changes → fixed and re-verified.** The reviewer found
  the card's stated scope (`GetVariantLabel`) was half the bug: the sibling
  reader `GetItemLabel`, one function above it in the same file, had the
  identical gap, reachable from the same handler. Folded into this diff
  rather than filed as a separate follow-up, since it's the same one-line
  pattern the card already established.

## The bug

`deleteMissing`'s FK-blocked retire-in-place mangles a unique column to
`"<value>~<id>"` on retire (`internal/data/sync_admin_repo.go`).
`stripRetireMangle` undoes this before a mangled value reaches a display
path. ut-docs#2316 found `CatalogRepo.VariantsForItem` was missing the
strip; independent review of that fix found a second, narrower-reach gap
this card was filed for: `CatalogRepo.GetVariantLabel` selects
`item_variants.sku` with **no `is_active` filter and no strip**, falling
back to the raw sku as `l.Code`. This is directly reachable:
`POST /api/print/labels` (`internal/pages/print_api.go`) accepts a
client-supplied `variant_id`/`item_id` with no ownership/active-state
check, and would print the mangled sku onto a physical shelf label.

This card's own independent review then found the identical pattern one
function up: `CatalogRepo.GetItemLabel` selects `items.sku` (also a
mangle-eligible unique column, per `sync_admin_repo.go`'s `adminTables`
entry `{name: "items", ..., unique: []string{"sku"}}`) with the same
missing filter/strip, reachable from the same `POST /api/print/labels`
handler via a client-supplied `item_id`.

## What shipped

- `internal/data/catalog_repo.go`:
  - `GetVariantLabel`: `l.Code = sku` → `l.Code = stripRetireMangle(variantID, sku)`.
  - `GetItemLabel`: `l.Code = sku` → `l.Code = stripRetireMangle(itemID, sku)`.
  Same idiom the rest of this file already uses for other mangle-eligible
  admin columns (`VariantsForItem`, `ListAllTaxCodes`, `GetLookup`, …).
- `internal/data/catalog_repo_crud_test.go` (new):
  - `TestGetVariantLabel_StripsRetireMangledSKU` — seeds a retired variant
    with a pre-mangled sku (`"S1-S~v1"`, `IsActive: false`), asserts
    `GetVariantLabel` returns `"S1-S"`.
  - `TestGetItemLabel_StripsRetireMangledSKU` — same shape for
    `GetItemLabel` (`"SKU1~i1"` → `"SKU1"`).

## What the independent review found, and what changed after it

Ran fresh against the diff as first submitted (variant-only fix):
`go build ./...`, `gofmt -l .`, `go test ./internal/data/... ./internal/pages/...`,
`golangci-lint run ./...`, `scripts/ci/guard-data-access.sh` — all green,
and confirmed the new test would have failed pre-fix.

One finding, ranked critical: **`GetItemLabel` has the exact same bug,
reachable from the exact same handler** (`internal/data/catalog_repo.go`,
then line 217) — `items` is mangle-eligible (`hasIsActive: true,
unique: []string{"sku"}`), `GetItemLabel` did `l.Code = sku` with no
filter/strip, and `POST /api/print/labels` calls it directly with a
client-supplied `item_id`. Assessed as in-scope for this card (not a
follow-up) since it's the same one-line pattern in the same file, found
while reviewing the exact function that motivated this card.

Two findings the review closed as no-action:

- `internal/pages/common/barcode_conflict.go`'s `l.Code` fallback:
  already covered — it calls `GetVariantLabel` directly, so it inherits
  the fix automatically. `l.Name` (the other half of that fallback) is
  never subject to the mangle in this codebase.
- `internal/pages/pos_modifiers_api.go`'s `GetVariantLabel` call: verified
  safe — `sellableVariants` only ever passes variant ids sourced from
  `ItemVariantsFor`/`ItemVariantsForSale`, both of which hard-filter
  `is_active = 1`, so a retired/mangled variant id can never reach it.

After folding in the `GetItemLabel` fix + its mirroring test: re-verified
TDD for real — reverted just the `GetItemLabel` fix line, re-ran
`TestGetItemLabel_StripsRetireMangledSKU`, confirmed it failed with the
exact predicted mismatch (`expected retire-mangle stripped from sku, got
"SKU1~i1"`), restored the line, confirmed it passed again. Then re-ran the
full gate on the combined diff: `gofmt -l .`, `go test
./internal/data/... ./internal/pages/...`, `go test ./...` (whole repo),
`golangci-lint run ./...`, `scripts/ci/guard-data-access.sh` — all green.

## Regression risk

`stripRetireMangle`'s own doc comment and `TestStripRetireMangle_OnlyExactOwnIDSuffix`
(`internal/data/strip_retire_mangle_test.go`) already document and accept
the one ambiguity — a real sku ending in exactly `"~"+its own row's id`
is indistinguishable from the mangle. Pre-existing, repo-wide accepted
tradeoff (same as every other `stripRetireMangle` call site in this file);
this card doesn't introduce or worsen it.

## Specific checks the review ran

- **Argument order/semantics**: `stripRetireMangle(id, mangled-value)`
  matches every other call site in this file exactly.
- **Backend-only, confirmed**: diff touches exactly one `.go` file plus
  its test, both under `internal/data` — no template/`web/`/locale file,
  so the UX checklist and help-topic-update requirement don't apply.
- **Repository-pattern/money**: no new SQL text outside `internal/data`;
  no money type touched.
- **Test data**: synthetic ids only (`i1`, `v1`), no real shop/client name.

## Explicitly deferred (not this card)

None — both readers named in ut-docs#2316's own review, plus the one
found reviewing this card, are now covered.
