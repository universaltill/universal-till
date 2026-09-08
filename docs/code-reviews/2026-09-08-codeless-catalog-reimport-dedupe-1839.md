# Code review: codeless-item duplicate on catalog re-import (ut-docs#1839)

**Date:** 2026-09-08
**Branch:** `fix/1839-codeless-catalog-reimport-dedupe`
**Reviewer:** independent Opus subagent (`complexity:medium` routing — Sonnet built, Opus reviewed), isolated worktree

## What shipped

A real SumUp café export (116 items) re-imported through `/api/import` went
116 → 230 items: `internal/pages/import_page.go`'s preview-time dedupe check
only ever compares a row against the catalog when it carries a SKU or a
barcode (`repo.BarcodeExists`/`repo.SKUExists`). A row with **neither** —
the normal shape of a SumUp export, since both columns are optional and
empty by default — was never compared at all, so it always inserted.

Fix:

- `internal/data/catalog_import_repo.go`: new `CatalogRepo.
  ItemExistsByNameAndCategory(ctx, name, department, category string)
  (bool, error)` — a **read-only** fallback identity check scoped by
  `(name, department, category)`. Mirrors `EnsureCategoryUnder`'s exact
  parent-scoped, trim + `COLLATE NOCASE` lookup, via a new private
  `findCategoryIDUnder` helper, but never creates a missing category as a
  side effect of checking one.
- `internal/pages/import_page.go`: the preview loop falls back to this
  check when a row has neither SKU nor barcode.
- `web/locales/{en,ar,fa,tr}.json`: new key
  `import.status.name_already_in_catalog`, styled like the sibling
  `barcode_already_in_catalog`/`sku_already_in_catalog` keys.
- `web/help/{en,ar,fa,tr}/catalog.md`: names the new dedupe key and its
  trade-off (see review finding below); screenshots regenerated
  (`make docs-shots`) since the guard hashes topic markdown content even
  though no screen's pixels changed.

Deliberately scoped by category, not name alone: two genuinely distinct
items sharing a name in *different* categories still both import. Two
items sharing *both* a name and a category collide — the same trade-off
`BarcodeExists`/`SKUExists` already make for a shared code.

## Independent review — verdict: SAFE TO MERGE

Full transcript kept by the orchestrator; summary here.

**TDD re-verified independently**, not taken on trust: the reviewer
reverted *only* the new fallback block in `import_page.go`, re-ran both new
tests with the fix still absent, confirmed the exact predicted symptom
(`TestImport_CommitLockReleasedAfterRequestFinishes_CodelessRowsDedupe`
fails, "got 2 want 1"; the repo-level unit test is unaffected by this
particular revert since it drives the repo method directly, not the
handler wiring — noted explicitly rather than assumed), then restored the
fix and confirmed both pass again. Beyond that, the reviewer
mutation-tested `ItemExistsByNameAndCategory` itself with three separate
mutations (always-false, drop category scoping, ignore parent scoping) —
all three were caught by the existing test.

**Verified beyond automated tests:**
- Re-derived the category-resolution logic against the commit path
  (`import_page.go`'s `ensureCategoryCached`) branch by branch
  (department+category, department only, category only, neither) and
  confirmed it mirrors commit-time nesting exactly.
- Independently re-confirmed the "same name, different categories" and
  "same name, same category" claims via the real `/api/import` handler,
  not just the repo-level test.
- Checked for the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll` on a file write; a cwd-relative path instead of
  `paths.Data(...)`) — both N/A, confirmed by grep.
- SQL injection: none (all parameterized). Nil-pointer safety on `catID`:
  confirmed safe. Non-ASCII / `COLLATE NOCASE` ASCII-only fold: confirmed
  consistent with `EnsureCategoryUnder`'s existing behavior, not a
  regression — and safer than a Unicode fold would be (avoids the Turkish
  dotless-i hazard).
- No real client/shop name in test data.

### Findings

**should-fix (fixed in this branch):** the user manual
(`web/help/en/catalog.md`) was not updated to mention the new third
dedupe key, or that a genuinely distinct item sharing both a name and a
category with an existing one is now silently skipped with **no override
path** (no `Import anyway` checkbox — verified `forceableImportIssue` is
never reached for this branch). Fixed: `catalog.md` in all four locales
now names this explicitly, with the same guidance (give the second item
its own barcode/SKU/category) the review recommended. No screenshot
content actually changed (screen layout unaffected), but the guard still
requires a regen since it hashes the topic's markdown.

**nit — coverage gap (fixed in this branch):** the original "same name,
different category" test passed for the weaker reason that the different
category didn't exist yet (an early-return in `findCategoryIDUnder`, not
the COUNT query itself). Added a case where that category is created
first, so the assertion now exercises the actual scoping logic the
COUNT query performs.

**nits — not fixed, judged genuinely out of scope or accepted:**
- Swallowed DB error (`exists, _ :=`) on the new check — identical to the
  adjacent, pre-existing `BarcodeExists`/`SKUExists` calls; consistent
  house style, not a regression.
- Quadratic cost on very large codeless imports (measured ~32x at 12,000
  rows vs. the SKU path) — real, but adds only ~0.3s at the documented
  2,000–3,000-row workload; not worth the added complexity for this fix.
  Worth a follow-up if a much larger single-file import becomes a real
  workload.
- In-file duplicate codeless rows are not deduped against each other
  (only against already-committed rows) — the card's actual bug
  (unbounded growth on *re-import*) is fixed; this is explicitly the
  territory of the separate SKU-synthesis-at-import-time follow-up
  already recorded as future work on the issue, not this card's scope.

## Verified beyond automated tests (orchestrator, before handoff to review)

- Full `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run
  ./...` (0 issues), full `go test ./...` (all packages green).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` all green.
- Drove the real `/api/import` handler directly (not just asserting on DB
  counts) and read the actual rendered HTML: the skipped row renders with
  the same `"muted"` row styling and Status-column text as the sibling
  barcode/SKU dedupe messages — no layout break, skip-count updates
  correctly ("0 ready to import, 1 will be skipped").
- Ran the one Playwright e2e spec touching import
  (`catalog-import-friendly-errors.spec.ts`) for real — passes.
- No local server/process left running afterward.

## Safe-to-merge

Yes. No blocker-class findings; should-fix and the coverage nit both
addressed in this branch before merge.
