# Code review: import preview predicts and lets the operator edit generated SKUs (ut-docs#3098)

- **Date:** 2026-10-03
- **Card:** universaltill/ut-docs#3098 — split from #3087 AC1 (preview half). A
  blank-SKU import row already got a generated SKU at commit (#3087); this
  card shows it in the preview, before commit, and lets the operator edit it.
- **Author:** Opus (Dev subagent), TDD-first.
- **Tester + UX:** separate subagent, real driven browser run (English +
  Farsi/RTL, 1024×600 kiosk floor + desktop, a real commit round-trip),
  screenshots taken and reviewed — reported "ready for review".
- **Reviewer:** Fable, independent, fresh context; re-derived from the diff
  itself, not from the Dev's or Tester's reports.

## What shipped

- `internal/data/item_sku.go`: `nextItemSKU`, `nextNumericCategorySKU`,
  `nextPrefixedSKU`, `skuTaken` take a `reserved map[string]bool`; a code in
  it counts as taken, `nil` is a no-op (Go nil-map read). Both real callers
  (`insertItemRow` in `catalog_repo.go`, `assignMissingItemSKUs` in
  `item_sku_backfill.go`) pass `nil`, so inserts and backfill behave exactly
  as before.
- `internal/data/item_sku_preview.go` (new):
  `(*CatalogRepo).PreviewItemSKU(ctx, department, category, reserved)` —
  read-only prediction. Resolves department (top-level) then category
  (under it) with the existing `findCategoryIDUnder`; found → `nextItemSKU`;
  not found (created only at commit) →
  `nextPrefixedSKU(skuPrefixFromName(category-or-department))`.
- `internal/pages/import_page.go`: on a non-commit, non-wizard preview, a
  per-request `skuReserved` map is seeded with every SKU/barcode already in
  the file; each non-skipped blank-SKU row gets a prediction, added to the
  map before the next row so sibling rows in one category predict distinct
  SKUs. Rendered as an editable, form-associated, labelled `row_sku_<Idx>`
  input alongside the existing `row_name_`/`row_price_` fields. At a staged
  commit, a row whose own SKU is blank takes the non-blank `row_sku_<i>` as
  its explicit SKU (collision → existing `ErrSKUExists` "already in
  catalog" skip); blank → unchanged commit-time generation. New status
  `import.status.created_generated_sku` names the SKU used (AC3).
  `confirmCarriedOverrideField` allowlist extended with `row_sku_<i>`.
- Locales: `import.preview.sku_generated`, `import.status.created_generated_sku`
  in en/ar/fa/tr (each with exactly one `%s`; ar/fa/tr are real translations).
- Help: one sentence appended to the import-SKU paragraph of
  `web/help/{en,de,ar,fa,tr}/catalog.md`.
- Tests: `internal/data/item_sku_preview_test.go`,
  `internal/pages/import_preview_sku_test.go`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | An operator can type into row A's field the exact code row B's kept prediction holds. Nothing warns at preview time; at commit row B hits `ErrSKUExists` and gets the clean, already-tested "already in catalog" skip (#1510 path). | Accepted as-is: preview can't see form edits that haven't happened yet, and the fallback is a translated, non-alarming status. A preview-side duplicate check across `row_sku_*` fields would be a small follow-up, not a blocker. |
| 2 | low | A row skipped for a forceable issue (missing name / unreadable price, "Import anyway") gets no generated-SKU field — prediction requires `!skipped`. Commit still auto-generates as before (#3087 behaviour, unchanged). | Accepted as-is: in scope is importable blank-SKU rows; a consistency follow-up could extend the field to forced-corrected rows later. |
| 3 | info | `predictSKUs` (`!commit && !wizardPreview`) is slightly broader than the render condition `interactive` (also needs a non-empty staged-upload id): when staging fails, predictions are computed but not shown. | Accepted: a handful of read-only SELECTs on an already-degraded path; no behaviour effect. |
| 4 | info | No length/charset validation on an operator-typed SKU. | Accepted: identical treatment to a file-carried SKU today — parameterised SQL, `htmlEscape` on every re-render (including the currency-confirm hidden inputs), UNIQUE enforced by the DB. Not new surface. |
| 5 | info | `en.json` gains two brand-new keys → `ut-plugin-language-{de,es}` need follow-up catch-up PRs (CLAUDE.md / reviewer skill: brand-new key → merge core first, pack PRs same cycle). | Orchestrator to-do, not a code finding. |

No fix was required in review; the working tree is exactly what Dev/Tester left.

## Verified beyond the automated gates

**TDD re-derivation (four claims; mutate → run → observe the semantic
failure → restore → run → pass), each confirmed byte-identical to a
pre-mutation snapshot afterwards:**

1. Removed `skuTaken`'s `reserved[code]` check →
   `TestSKUGenerator_ReservedTreatedAsTaken` fails on 4 subtests with the
   exact wrong-SKU/`skuTaken` assertions the test names. Restored → pass.
2. Removed the `skuReserved` seeding loop (file's own SKUs/barcodes) →
   `TestImport_PreviewGeneratedSKUSkipsInFileSKUs` fails
   ("generated SKU = FRU-0001, want FRU-0002"). Restored → pass.
3. Changed the category lookup's parent arg to `""` →
   `TestPreviewItemSKU/category_is_scoped_under_its_department` fails
   (wrong category's sequence picked up). Restored → pass.
4. Dropped the `it.SKU == ""` guard on the commit-time `row_sku_<i>` read →
   `TestImport_CommitIgnoresSKUFieldForRowWithOwnSKU` fails (a row's own SKU
   got hijacked by a stray form field). Restored → pass.

All four failures were the semantic assertion the test exists for, never a
compile error or unrelated breakage. After full restore: `git diff`/
`git status --porcelain`/untracked-file hashes identical to baseline;
`go test ./internal/data/...` and the new `internal/pages` tests plus
`TestImport_CategoryAndTaxCodeLookupsAreCachedPerRun` all pass; `gofmt -l`
and `go vet` clean on both packages.

**Preview/commit divergence check:** `PreviewItemSKU` and the commit path's
`ensureCategoryCached`/`EnsureCategoryUnder` use the identical
trim+`COLLATE NOCASE`+parent-scoped lookup (`findCategoryIDUnder`), and the
ASCII-only cache fold matches NOCASE on purpose (#1322 B1). A brand-new
category has no items in either path, so rule 1 (numeric sequence) can
never diverge; rule 2 uses the same trimmed name in both. A stale
prediction from a concurrent insert between preview and commit is caught by
the existing `ErrSKUExists` skip — correctness never depends on the
prediction holding.

**Concurrency:** `skuReserved`/`predictSKUs` are declared inside the
request handler closure, fresh per request, never package-level, never
shared across goroutines — confirmed by grep (only the one request's scope
references them). Two racing preview requests each build their own map; the
DB is read-only in this path.

## Confirmed

- **Money:** n/a — no `money.Money`/amount handling touched.
- **i18n:** every new string via `T(...)`; no hardcoded English; keys
  present with matching `%s` in en/ar/fa/tr; only logical CSS
  (`margin-block-start`), RTL-safe.
- **Offline-first:** n/a — till-local SQLite reads only, no network call,
  no new blocker/modal.
- **Plugin signing:** n/a — no plugin surface touched.
- **Disk-write bug patterns** (missing `os.MkdirAll`, cwd-relative path):
  n/a — nothing in the diff writes to disk.
- **Shared/global mutable state:** none added.
- **Help topic:** reads naturally and accurately in English and German;
  no existing screenshot of this surface to go stale.
- **Real shop names / secrets:** none in test data or docs.

## Verdict

Safe to merge. No defects found; the two low-severity items are accepted
behaviours with clean, already-tested fallbacks and can become small
follow-up cards if wanted later.

## Orchestrator to-dos (not code findings)

- Merge core first (this PR), accept `main` going red on `lang-pack-drift`
  (expected for a brand-new key), then land catch-up PRs in
  `ut-plugin-language-de` and `ut-plugin-language-es` in the same cycle.
