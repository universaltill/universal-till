# Code review: variant-tracked items' stock reaches low-stock/reorder signals (ut-docs#2082)

**Card:** universaltill/ut-docs#2082 — "Low-stock signals never see a
variant-tracked item's stock (GetLowStockItems/ListStockLevels join on
item_id)"
**Complexity:** medium (Dev: inline, session model Sonnet; Review: Opus
subagent, fresh context, independent from the Dev pass)

## The bug

`GetLowStockItems`/`ListStockLevels` (`internal/data/pos_repo.go`) joined
`inventory` to `items` on `item_id` alone. A variant-tracked item's
inventory rows carry `item_id NULL` / `variant_id` set instead (the
`001_init.sql` CHECK constraint), so they never matched: a variant-tracked
item read as permanently out of stock at a false qty of 0 once its
`reorder_level` was set, regardless of what its variants actually held, and
never got a real value in `/inventory`'s "Reorder at" column.

## What shipped

Extended `GetLowStockItems`/`ListStockLevels` with the same additive shape
ADR-0043 already established for `StockForExport`/`variantStockForExport`:
new sibling queries (`variantLowStockItems`, `variantStockLevels`) joined
through `item_variants`, appended to the existing item-scoped rows — never
folded/summed into the parent item's own row (ADR-0043 Decision 3, no
double-counting). `LowStockItem` gained `VariantID`/`VariantName`
(`omitempty`), same convention as `ExportStockRow`.

`GetLowStockItems`'s item-scoped branch also gained a guard so a
variant-tracked item with no item-scoped inventory row of its own no longer
phantom-reports itself as "never stocked" (qty 0) at every location — its
real stock is reported by its variant rows instead.

`StockForExport` was touched only to skip `ListStockLevels`' own new
variant rows (`if l.VariantID != "" { continue }`) so it doesn't
double-count them against its own, separately-filtered
`variantStockForExport` — that function itself is byte-identical to
before (confirmed by the independent review, see below).

Two templates (`web/ui/pages/inventory.html`,
`web/ui/partials/stock_table.html`) and the low-stock HTML-fragment
builder (`internal/pages/inventory_api.go`) show a variant's own name
alongside its parent's. `web/help/en/inventory.md` updated;
`reference/plugin-manifest.md` (ut-docs repo, companion PR) corrected a
now-stale claim that `/inventory` stayed item-only.

Tests: `internal/data/pos_repo_variant_low_stock_test.go` (new),
`internal/data/pos_repo_batch8_inventory_test.go` (updated — the existing
test asserted the OLD buggy exclusion as correct), plus fixes to callers
whose fixtures already had both an item-level row and a variant
(`internal/pages/ask_api_test.go`, `internal/pages/cloudsync_wire_test.go`)
and two hand-rolled minimal test schemas that needed an `item_variants`
table added (`internal/pos/inventory_test.go`,
`internal/pos/offline_resilience_test.go`).

## Independent review (Opus, fresh context, isolated worktree)

Spawned via `Agent` with `model: opus`, pointed at a detached-HEAD git
worktree of the fix commit (never touched the orchestrator's own shared
checkout). Full report is in the PR; summary here.

**Verified independently, not just re-stated:**
- `go build`/`go vet`/full `go test ./...` clean.
- `variantStockForExport` byte-identical pre/post-diff (md5-compared) —
  the export contract (ADR-0043) is genuinely untouched.
- TDD re-verification via a surgical behavioural revert (kept the new
  test files, reverted only the query logic): `TestGetLowStockItems_VariantTrackedItem`
  and `TestListStockLevels_Batch8` both failed with the bug's exact
  signature (a phantom qty-0 row for the parent, no variant rows at all);
  restored and both passed again; worktree left clean.
- Relevant CI guards (`guard-data-access`, `guard-i18n`, `guard-kiosk-engine`,
  `guard-help-topics`, `guard-help-drift`, `guard-docs-shots`,
  `guard-page-http-error`) all green.
- Confirmed no money/checkout-path/file-I/O code touched, rather than
  assuming it from the diff's description.

**Two blockers found, both fixed in a follow-up commit on this branch:**

1. **`internal/cloudsync/cloudsync.go`'s catalog-sync qty builder**
   (`qty[l.ItemID] += l.CurrentQty` over `ListStockLevels`' rows) was
   unguarded — a caller the original diff's own grep-the-callers pass
   missed. Now that `ListStockLevels` returns variant rows carrying their
   parent's `ItemID`, this silently folded a variant's stock into the
   parent's *pushed cloud quantity*, exactly the double-counting
   ADR-0043 forbids. Fixed with the same `VariantID != ""` skip
   `StockForExport` already needed; the adjacent comment describing "no
   qty on variants here" was also stale and corrected. New regression
   test `TestPushSnapshotIfChangedExcludesVariantQtyFromParent`
   (`internal/cloudsync/cloudsync_test.go`) — measured 12 (5+4 folded)
   pre-fix-shape vs. 5 (correct) post-fix.
2. **A variant row was clickable** (`class="stock-row"`) and opened the
   receive/adjust dialog via the existing delegated click listener, but
   that dialog has no `variant_id` field at all (`grep -n variant
   web/ui/pages/inventory.html` found only the new display attribute) —
   tapping one would silently record a receipt/adjustment against the
   *parent's* item-level stock, leaving the actually-low variant
   untouched. Fixed by skipping variant rows (`row.dataset.variant`) in
   the click handler until that dialog supports picking a variant, and
   documented the limitation in the manual rather than claiming a
   workaround that doesn't exist (an earlier draft of the doc line
   wrongly implied the item picker could select a variant — checked the
   picker's own markup, it can't, corrected before committing).

**Two real, non-blocking findings — one fixed, one filed as a follow-up
card rather than expanding this diff's scope:**

3. **Fixed:** `GetLowStockItems`'s "does this item have a variant at all"
   guard didn't scope to *active* variants, so an item whose only variant
   had since been deactivated fell through both the item-scoped branch
   (suppressed — "a variant exists") and the variant branch (excludes
   inactive variants) and vanished from the reorder list entirely, a
   real regression vs. pre-fix behavior for that one shape. Scoped the
   `NOT EXISTS` subquery to `v.is_active = 1`; new regression test
   `TestGetLowStockItems_OnlyVariantInactive`.
4. **Filed as ut-docs#2089**, not fixed here: `ItemDailySellRates` returns
   one sell rate per item (summed across variants), but each new variant
   row now gets the item's *whole* rate applied against just that one
   variant's quantity in `internal/pages/inventory_page.go`,
   `internal/alerts/alerts.go`, `internal/pages/reports_page.go` — a
   healthy, well-stocked variant item can report every variant as
   "running out" individually and suggest over-ordering. This is a
   days-left/order-suggestion *prediction* concern, not the
   qty/reorder-level *correctness* this card's own acceptance criteria
   was scoped to, so it's tracked separately rather than silently
   widened into this diff.

**Two nitpicks, both addressed:**
5. `data-name` on a variant row stayed the parent's bare name while the
   visible cell showed "Parent — Variant", so the stock-table search
   filter couldn't match a typed variant name. Made `data-name` mirror
   the visible text in both templates and the HTML-fragment builder.
6. `TestListStockLevels_VariantTrackedItem_ExcludesUntracked` passed
   against the pre-fix code too (it only ever asserted a variant row
   must be *absent*, vacuously true when none exist) — added a positive
   control (a properly-tracked sibling's variant row that must still
   appear).

Two further nitpicks (row ordering not literally interleaved by name
despite the manual's "alongside" wording; minor schema-completeness gaps
in two hand-rolled test fixtures) were read and accepted as genuinely
low-priority — not fixed, not worth the added complexity/scope for this
card.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` clean on the final tree.
- `go test ./...` (whole module) — all green, re-run after every fix
  round, not just once.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-engine.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`
  (re-recorded the baseline's English signature for the `inventory` topic
  — the help-doc edit legitimately shifted its bullet/bold-leadin counts),
  `guard-docs-shots.sh` (`make docs-shots` run twice, once per round of
  template edits, using this session's pre-installed Chromium — ut-docs#622) —
  all green on the final tree.
- Independent TDD re-verification (revert → fail with the bug's exact
  signature → restore → pass) performed by the review subagent, not just
  asserted by the Dev pass.

---
_Generated by [Claude Code](https://claude.ai/code)_
