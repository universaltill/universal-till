# Code review — CatalogRepo.VariantsForItem leaks a retire-mangled sku (ut-docs#2316)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2316 (`complexity:easy`)
- **Branch:** `fix/2316-variant-sku-retire-mangle-leak`
- **Reviewer:** independent pass, fresh-context Sonnet subagent working in
  an isolated worktree (per this card's `complexity:easy` routing — a
  clean instance that never saw the dev reasoning, per `MODEL-ROUTING.md`'s
  "different model relaxes to different instance" rule for easy cards).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking findings; one nit folded
  in, one should-fix filed as a follow-up (ut-docs#2355, out of scope for
  this narrowly-scoped card).

## The bug

`deleteMissing`'s FK-blocked retire-in-place mangles `item_variants.sku`
to `"<sku>~<id>"` on retire (`internal/data/sync_admin_repo.go`).
`stripRetireMangle` exists to undo that before a mangled value reaches a
display path, and every reader swept after ut-docs#1610's review calls it.
`CatalogRepo.VariantsForItem` (`internal/data/catalog_repo.go`) —
the data source for the catalog admin per-item edit panel — deliberately
also returns retired variants (so staff can see/manage them), and was
scanning `sku` with no `stripRetireMangle` call: a retired variant with a
mangled sku showed the raw `"S1-S~v1"` garbage in the UI. Every other
`item_variants.sku` reader (`ItemVariantsFor`, `ItemVariantsForSale`,
`variantLowStockItems`, `variantStockForExport`) filters `is_active = 1`
and is unaffected — `VariantsForItem` is the one intentional exception.

Not a regression from ut-docs#2273's fix — a pre-existing gap in the
original ut-docs#1610 sweep, surfaced by that review's own reviewer while
reading adjacent code.

## What shipped

- `internal/data/catalog_repo.go`: one line inside `VariantsForItem`'s
  scan loop, `v.SKU = stripRetireMangle(v.ID, v.SKU)`, applied right after
  the row scan and before the `byID`/`out` bookkeeping — same idiom this
  file already uses elsewhere for other mangle-eligible admin columns.
- `internal/data/catalog_repo_crud_test.go` (new):
  `TestVariantsForItem_StripsRetireMangledSKU` — seeds a retired variant
  with a pre-mangled sku (`"S1-S~v1"`, `IsActive: false`) and asserts
  `VariantsForItem` returns the stripped value (`"S1-S"`).

## What the independent review found

Ran the full gate itself: `go build ./...`, `go vet ./...`,
`go test ./internal/data/... -run TestVariantsForItem -v`, the full
`internal/data` package suite, `gofmt -l internal/data/`, and
`golangci-lint run ./internal/data/...` — all green.

**TDD re-verification, done for real by the reviewer independently**:
commented out the fix line, re-ran `TestVariantsForItem_StripsRetireMangledSKU`,
confirmed it failed with the exact predicted mismatch
(`expected retire-mangle stripped from sku, got "S1-S~v1"`), restored the
line, confirmed it passed again with a byte-identical diff back to the
original commit.

Confirmed no new SQL text is introduced (a pure Go-side field mutation in
`internal/data`, exactly where this belongs per the repository-pattern
rule) and no file-write/path-construction bug class applies (no
`os.Create`/`WriteFile`/`MkdirAll`/`paths.*` anywhere in the diff).

Two findings:

1. **should-fix, filed as a follow-up card (ut-docs#2355), not folded into
   this diff** — `CatalogRepo.GetVariantLabel` (`internal/data/catalog_repo.go`)
   also selects `item_variants.sku` with no `is_active` filter and no
   `stripRetireMangle` call, falling back to the raw sku as `l.Code`. This
   is reachable: `POST /api/print/labels` accepts a client-supplied
   `variant_id` with no active-state check and can print `label.Code`
   (unstripped) onto a physical shelf label; a lower-severity second
   instance exists in `internal/pages/common/barcode_conflict.go`'s
   fallback message. Genuinely a different reader than the one this card
   named (`VariantsForItem`) and a display-path sibling of the sync-write
   gap already tracked as ut-docs#2273 — correctly out of this narrowly-
   scoped card, filed separately rather than silently expanding the diff.
2. **nit, fixed** — the new test's comment inaccurately implied
   `VariantsForItem` already strips `item_variants.name` "a few lines
   away." It doesn't (and shouldn't — `name` isn't in this table's
   mangle-eligible `unique` column list per its `sync_admin_repo.go`
   `adminTable` entry). Reworded to reference that entry instead of a
   nonexistent sibling call.

After folding in finding 2: re-ran `go test ./internal/data/... -count=1`
and `gofmt -l internal/data/catalog_repo_crud_test.go` — clean.

## Specific checks the review ran

- **No other affected reader missed within scope.** Grepped every
  `item_variants` read for `sku` exposure without an `is_active` filter;
  the four readers the ticket already named are correctly unaffected
  (all filter `is_active = 1`). `GetVariantLabel` is the one gap found,
  filed separately (above) rather than folded in, since ut-docs#2316's
  stated scope is `VariantsForItem` only.
- **Backend-only, confirmed**: diff touches exactly one `.go` file plus
  its test, both under `internal/data` — no template/`web/`/locale file,
  so the UX-guidelines checklist and help-topic-update requirement don't
  apply.
- **Test data / secrets**: clean — synthetic ids only (`i1`, `v1`), no
  real shop/client name, no secret-shaped literal.

## Explicitly deferred (not this card)

- ut-docs#2355 — `GetVariantLabel` doesn't strip the retire-mangle from
  `sku`, reachable via `POST /api/print/labels` with an operator-crafted
  `variant_id` (finding 1 above).
