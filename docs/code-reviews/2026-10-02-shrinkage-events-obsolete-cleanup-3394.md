# Review — shrinkage_events no longer breaks catalog cleanup / sample-data removal (ut-docs#3394)

- Date: 2026-10-02
- Lane: lane:cloud-24 (scheduled build routine)
- Branch: `fix/3394-shrinkage-events-obsolete-cleanup`
- Complexity: easy — built by Sonnet (dev subagent), reviewed by Opus 5.5 in a fresh context (MODEL-ROUTING)

## What shipped

`shrinkage_events.item_id` (035) is a FOREIGN KEY to `items(id)` with no
ON DELETE action. An item only ever voided / comped / wasted has a
shrinkage row but no `sale_lines` / `stock_movements`, so it looked
"never sold": Catalog cleanup and Remove sample data tried to delete it,
hit the FK, and rolled back the whole purge.

Mirrors the ut-docs#3340 fix for `age_verifications` (live table only —
`shrinkage_events` is not cleared or archived by reset, so there is no
`_archive` clause to add):

- `internal/data/pos_repo.go` — `obsoleteItemsPredicate` excludes items with a shrinkage row.
- `internal/data/demo_seed_repo.go` — `demoItemReasonCaseSQL` classifies them as `history` (covers `keptDemoItems` and `RemoveDemoItem`); doc comments updated.
- `internal/data/seeddata/remove_demo.sql`, `remove_demo_relaxed.sql` — same exclusion.
- Help: `web/help/en/catalog.md`, `web/help/{en,de,tr}/display.md` — the "always kept" list now names voided/comped/waste items and refused ID checks (the #3340 behaviour, previously undocumented).

## Tests (TDD)

- `TestCleanupObsoleteItems_KeepsItemWithOnlyAShrinkageEvent` (includeActive false/true)
- `TestRemoveDemoCatalogueKeepsShrinkageItem` (strict false/true)
- `TestRemoveDemoItemRefusesShrinkageItem` (added after review)

Re-verified by the reviewer in an isolated worktree with the non-test files
reverted to `origin/main`: `RemoveDemoCatalogue` → `FOREIGN KEY constraint
failed (787)`; `CleanupObsoleteItems` (preview assertion disabled) →
`cleanup items: constraint failed: FOREIGN KEY constraint failed (787)`;
`RemoveDemoItem` → raw FK error instead of `ErrDemoItemHasHistory`. All
pass on the branch.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (follow-up) | Clear transaction history never clears/archives `shrinkage_events`, so a training void pins the item against cleanup forever | Filed ut-docs#3452 (needs an Architect call on a reset-archive twin) |
| 2 | nit | Help text didn't say voided/comped/wasted (or ID-check-refused) items are kept | Fixed (en catalog/display, de/tr display) |
| 3 | nit | `ErrDemoItemHasHistory` doc listed only sale/stock/parked | Fixed |
| 4 | nit | 035's comment claims the row "must survive the referenced item's deletion" — the plain FK actually blocks it | Accepted (pre-existing, shipped migration) |
| 5 | nit | No `item_id` index on `shrinkage_events`; uncorrelated `NOT IN` runs once per cleanup, correlated `EXISTS` ≤ ~52 demo items | Accepted — rare admin action, same shape as age_verifications |
| 6 | nit | No committed test for `RemoveDemoItem` on a shrinkage-only item | Fixed (test above) |

Reviewer also confirmed every `DELETE FROM items` path (5 sites) is covered,
`shrinkage_events` has no `variant_id`, and no other un-cascaded FK to
`items` is unhandled.

## Gate

`gofmt -l .` empty; `go build ./...`; `go vet ./...`; `go test ./...` all
green; guards: data-access, core-neutral, migration-version-collision,
i18n, help-drift, help-topics, compliance-claims, competitor-naming.
No UI surface changed (backend SQL + help prose), so no driven visual run.

## Verdict

Safe to merge.
