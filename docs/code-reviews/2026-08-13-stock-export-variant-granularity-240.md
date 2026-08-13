# Code review: stock export includes variant-scoped inventory

**Date:** 2026-08-13
**Branch:** `feat/240-variant-stock-export`
**Closes:** universaltill/ut-docs#240 ("Stock level export silently omits variant-scoped inventory")
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model), fresh context

## What shipped

`internal/data/export_repo.go`'s `StockForExport` (the `export.requested.ask` plugin-dispatch payload's `stock` field, ut-docs#59) now appends variant-scoped inventory rows to the existing item-scoped ones, via a new `variantStockForExport` query. `inventory` rows are either item-scoped (`item_id` set, `variant_id` null) or variant-scoped (`item_id` null, `variant_id` set) — the CHECK constraint in `001_init.sql`; the existing `ListStockLevels` (unchanged, still serves the `/inventory` page) only ever matched item-scoped rows via its `INNER JOIN items`. A variant row carries `ExportStockRow`'s new `VariantID`/`VariantName` fields, its own SKU (not the parent's), and the parent item's `ItemID`/`Name` so a plugin can group it under its parent — never merged into or double-counted against the parent's own row.

Also: `internal/cloudsync/cloudsync.go`'s comment and `ut-docs/reference/plugin-manifest.md`'s stock contract section both cited "ADR-0011" as the reason variant stock stayed item-level-only. That citation was wrong — ADR-0011 (`ut-docs/adr/0011-multi-till-sync.md`) is multi-till sync/ownership and says nothing about item-vs-variant reporting granularity. New `ut-docs/adr/0043-stock-export-variant-granularity.md` records the real decision (a standalone new ADR, not an amendment — ADR-0011/ADR-0036 are unaffected) and corrects the citation.

## Independent review

Spawned a `general-purpose` subagent with `model: opus`, fresh context, briefed with the exact diff scope, the relevant `CLAUDE.md` rules, and told to run the full gate and mutation-test the new assertions itself.

**Note on process:** the first review round was killed mid-run after an unrelated operator error (`git reset --hard` executed against this branch while re-verifying an earlier merge in the same session, wiping the then-uncommitted diff) risked contaminating its shared-checkout view. The diff was reconstructed byte-for-byte from what had already been shown in-session and re-verified against the full gate before a **second, fresh** review ran against the clean, restored state. That second round is the one whose findings are recorded below; the first round's partial output was discarded rather than trusted.

**Real findings, fixed before merge:**

- **Blocking — duplicate ADR-0042 index row.** The ADR-0043 commit accidentally introduced a second, reworded `adr/README.md` row for ADR-0042 (unrelated to this ticket — a rebase/edit artifact) alongside the pre-existing one, with ADR-0043 sandwiched between them out of order. Fixed: removed the duplicate, ADR-0043 now sits after the single ADR-0042 row.
- **Blocking — the "corrected" `cloudsync.go` comment swapped one wrong rationale for another.** The original comment ("repeating the parent qty would double-count") was accurate for its own point; the in-session rewrite replaced it with an equally-wrong claim ("adding a variant's qty on top would double-count"). In fact `cloudsync.go`'s per-item `qty` map is built solely from `ListStockLevels`, which is item-scoped only — a variant's own qty was never in that map, so surfacing it there wouldn't double-count anything; it's simply not done in that cloud-sync surface today (a separate, accepted gap, unrelated to and unaffected by this card). The same wrong framing had been copied into ADR-0043 §5. Both fixed with the accurate rationale.
- **Should-fix — half the active-item filter was untested.** `variantStockForExport`'s `WHERE i.is_active = 1 AND v.is_active = 1` had a fixture only for an inactive *variant*; mutation-testing (deleting `i.is_active = 1`) still passed the whole `internal/data` package. Added `TestStockForExport_VariantScoped_InactiveParent` (active variant, inactive parent → row absent); re-ran the same mutation, now fails as expected.
- **Should-fix — a variant row's `reorder_level` is the *parent's* threshold, undocumented.** `item_variants` has no `reorder_level` column, so the query mirrors the parent's. Added a clarifying sentence to `plugin-manifest.md` so a plugin author doesn't read a variant row's `reorder_level` as variant-specific.
- **Should-fix — no wire-level test for the new JSON fields.** The original tests were repo-level only; a tag typo or a missing `omitempty` could have shipped silently. Added `TestExportDispatch_PayloadIncludesVariantStockData` in `internal/pages/export_dispatch_test.go`, unmarshaling the real dispatched payload into a generic map to assert both that `variant_id`/`variant_name` round-trip correctly on a variant row *and* that they're omitted entirely (not just empty) on an item-level row. Mutation-tested by renaming the `variant_id` JSON tag — fails as expected.

**Verified correct (no changes needed):** no double-counting (CHECK constraint makes item-/variant-scoped rows disjoint; fixture exercises the risky case — parent and variant both stocked at the same location); NULL SKU handled via `sql.NullString` (`item_variants.sku` is nullable); repository pattern clean (new SQL confined to `internal/data`); `/inventory` page unchanged (`ListStockLevels` untouched, diff is 3 code files); empty-slice contract preserved (`stock` still marshals as `[]`, never `null`); the ADR-0011 correction is itself accurate (reviewer read `0011-multi-till-sync.md` directly — zero mentions of "variant").

**Noted, not acted on (non-blocking):** payload ordering is item-rows-then-variant-rows, not globally sorted — harmless and undocumented; both new fixtures use a single location (`loc1`) — multi-location variant stock and the `sl.name` ordering tiebreak remain untested; the ADR-0011 mis-citation still stands verbatim in two older, point-in-time review records (`2026-08-01-export-stock-level-data.md`, `ut-docs`' `2026-07-19-variants-in-snapshot.md`) — ADR-0043 names the #59 review as a propagation site but deliberately doesn't rewrite history.

## Full gate (final, post-fix)

`go build ./...`, `go vet ./...` — clean. `go test ./internal/data/... ./internal/cloudsync/... ./internal/pages/... -race` and the full `go test ./... -race` — all green. `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` — all green (no template/UI changes in this diff, so the docs-shots surface hash is unaffected).

## Verdict

**Safe to merge.** Both blocking findings and all three should-fix findings were addressed and independently re-verified (mutation-tested) in-session; nothing was deferred that changes this ticket's own acceptance criteria.
