# Code review — shrinkage_events joins the reset archive (ut-docs#3452)

- **Date:** 2026-10-02
- **Branch:** `fix/3452-shrinkage-reset-archive`
- **Author lane:** `lane:cloud-24` (built on Opus 5.5)
- **Reviewer:** independent subagent on Fable (different model from the author)

## What shipped

Settings → Data → **Clear transaction history** (ADR-0042) moved every
transactional table into its `*_archive` twin except `shrinkage_events`, so
a void/comp/waste rung up while training stayed in the Shrinkage & Loss
report and (since #3394) pinned its item against Catalog cleanup and Remove
sample data for good.

- `063_shrinkage_events_archive.sql`: archive twin, same shape as 057
  (column-identical + `reset_batch_id`, no PK, no FKs to live tables, CHECK
  and NOT NULLs kept), with batch and item_id indexes. Pinned in
  `shipped_migrations_test.go`.
- `resetArchiveTables` gains `shrinkage_events` (no FK to sales, so position
  isn't load-bearing). Reset archives it, restore re-inserts it, purge
  (`DeleteResetBatch`) clears it.
- `restoreEmptyCheckTables` gains `shrinkage_events`: a void needs no sale,
  so a post-reset void would otherwise be merged with the restored batch
  (ADR-0042 §2), same reasoning as `worker_allocations`.
- `_archive` clauses wherever the live `shrinkage_events` clause existed:
  `obsoleteItemsPredicate`, `demoItemReasonCaseSQL`, `remove_demo.sql`,
  `remove_demo_relaxed.sql`, `itemStockHistorySQL` (args 5→6). An item whose
  only reference is an archived void is kept until its batch is purged —
  the same rule as `sale_lines_archive`.
- `nonAdminTables` classification for the new table (schema-drift test).
- Shrinkage reports keep reading the live table only (a reset empties the
  report; a restore refills it).
- Manual: `web/help/en/reports.md` Shrinkage section explains the reset,
  restore and cleanup behaviour; topic hash refreshed in
  `web/help/img/manifest.json` (prose only, no pixel change).

## Tests (TDD)

`internal/data/shrinkage_reset_test.go`: full-column round trip including
NULLs; restore refused after a post-reset void; archived void keeps the item
for cleanup (both modes) and single-item delete until the batch is purged,
then cleanup removes it; restore refuses with `ErrArchiveReferencesRemoved`
when the item was deleted behind the predicates' back.
`demo_seed_repo_test.go`: Remove sample data (strict + relaxed) and the
per-item path keep an item with an archived void as "history".

The reviewer re-verified the TDD claim: reverting `reset_archive_repo.go`
alone fails three of the new tests; reverting the predicate files alone
fails the demo and cleanup tests; restoring makes all pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | A post-reset void blocks Restore with "traded since" although nothing was sold — consistent with ADR-0042 §2 but not obvious to an operator | Fixed: help paragraph says so |
| 2 | nit | `RestoreResetBatch` doc comment listed only four check tables (already stale) | Fixed: points at `restoreEmptyCheckTables` |
| 3 | nit | New help paragraph is English-only | Accepted: de/tr/ar/fa `reports.md` lack the whole Shrinkage & Loss section (pre-existing, baselined drift, ut-docs#1962), so there's nothing to anchor it to |
| 4 | nit | No test for the `ErrArchiveReferencesRemoved` backstop on shrinkage | Fixed: `TestRestoreRefusesWhenArchivedShrinkageItemRemoved` |

Reviewer also confirmed: no later ALTER on `shrinkage_events`; no LAN-sync,
export, DSFinV-K or GDPR path reads it; nothing in `internal/pages` or `e2e/`
assumes the old live-only behaviour. Pre-existing, out of scope: hardcoded
English restore/purge error strings in `internal/pages/data_api.go`.

## Verified beyond unit tests

`go build ./...`, `go vet`, `golangci-lint` (data/db: 0 issues), the
`ci.yml` build-job guards (all pass; `shellcheck` not installed locally, and
no shell script changed), `go test ./...`. The first full run showed three
`internal/pages` failures (`TestDemoRouteClassification`,
`TestFiscalDevicePage_ConfirmAndUnpairFlipTheGateFlagWithAudit`,
`TestSelfOrderShop_CannotSignRefusesCheckout`) while the reviewer's own test
run was competing for CPU. The diff doesn't touch `internal/pages`; all
three pass on their own and the whole package passes on a rerun (180s), so
CI on the PR is the deciding check. No UI surface changed apart from
help prose, so no screenshots were taken.

## Verdict

Safe to merge. No deferred work.
