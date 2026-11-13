# Review: delete a never-sold item that has comps, wastes or ID checks (ut-docs#4778)

Follow-up of ut-docs#4746 (universal-till#2094). Owner rule (2026-10-22): an
item that was never sold must be deletable.

Scope: `internal/data/item_delete_repo.go`, the two audit writers
(`internal/pages/cloudsync_catalog_wire.go` `cloudDeleteItem`,
`internal/catalogpull/apply.go` tombstone), help topic `catalog` (en/de/tr),
`web/help/img/manifest.json`. Companion: ut-docs `reference/manage-shop-catalog-api.md`
§3.10. Built by Sonnet 5.5 (Dev subagent); reviewed by Opus 5.5 (this
session, a different model).

## What changed
- `deleteUnusedItemTx`: live comps and wastes no longer refuse
  (`ItemUseShrinkage` and `ItemInUseError.Count` removed); live ID checks no
  longer refuse. `itemAgeCheckSQL` now reads `age_verifications_archive` only,
  under the new reason `archived_age_check` ("its ID checks sit in an
  archived reset that can still be restored — deactivate it instead").
  Archived shrinkage (`archived_shrinkage`), sold, real stock movement, quick
  button, parked sale and open vouchers refuse as before, all before any
  write.
- `deleteUnusedItemWrites` (inside the existing `SAVEPOINT
  delete_unused_item`): counts live `shrinkage_events` per category, detaches
  them all (`item_id = NULL`, `item_name` kept), and detaches live
  `age_verifications` likewise. Both columns are nullable (migrations 035,
  056), so there is no migration.
- `ItemDeleteResult` gains `Comps`, `Wastes`, `IDChecks`, plus
  `DetachedNote()` ("item deleted with 2 voids, 1 comp, 1 waste, 1 ID check",
  singular/plural per kind) and `AddDetachedToAudit` (`voids`, `comps`,
  `wastes`, `id_checks` only when > 0, plus `note`). Both audit writers use it,
  so the two delete paths' audit rows read alike. The old note said "1 voids";
  it now says "1 void".
- Help: the "Deleting a never-sold item in Manage shop" bullet now states the
  new rule in en/de/tr. The structure is unchanged, so `guard-help-drift` stays
  green. ar/fa are untouched; they were already behind, per the baseline.

## Review findings
1. **Checked, fine: restore safety.** A restore of a reset batch needs every
   archived row's `item_id` to exist (`ErrArchiveReferencesRemoved`). Archived
   shrinkage and archived ID checks still refuse. Live rows that are detached
   now carry `item_id = NULL`; a later reset archives them with NULL, and the
   archive tables accept it.
2. **Checked, fine: report totals.** The Shrinkage report
   (`ShrinkageByReason`, `ShrinkageTopItems`) and the ID-check log
   (`age_verification_repo.go`) read `item_name` and never join `items` on
   `item_id`. Neither table is uploaded by cloudsync. A test asserts the report
   totals are identical before and after the delete.
3. **Checked, fine: atomicity.** The count, detach and delete all run in one
   immediate-lock transaction and one savepoint. A late refusal (the
   `obsoleteItemsPredicate` final `DELETE`) rolls the detaches back, including
   inside the catalogue pull's committing transaction. That case is covered by
   `TestDeleteUnusedItemTx_ArchivedAgeCheckRefusesAndChangesNothing_4778`.
4. **Checked, fine: the cleanup predicate is unchanged.** The bulk Catalog
   cleanup still keeps an item with any comp, waste, void or ID check, which
   matches its help bullet. The owner's rule is about deleting one item.
5. **Low, fixed:** a test comment line ran past the file's wrapping.
6. **Accepted:** the Dev step wrote the `en`/`tr` `catalog` topic hashes in
   `web/help/img/manifest.json` by hand instead of running `make docs-shots`.
   This environment has no pinned-font Playwright image. Each hash is
   sha256 of the topic markdown, exactly what the generator writes (see
   `guard-docs-shots.sh` `topic_hash`). The help prose appears in no screenshot,
   which captures app routes only, so no PNG would change.
   `surface_sha256` came from `update-docs-shots-surface-hash.sh`. The guard
   passes.

## TDD (re-verified by the reviewer)
The new tests in `internal/data/item_delete_detach_4778_test.go` were run
against the old implementation, with an API shim so they compile, in a
throwaway worktree. These five failed:
`DetachesEachKind`, `MixedDetachNote`, `DetachedNoteForms`,
`ShrinkageReportTotalsUnchanged` and `ArchivedAgeCheckRefusesAndChangesNothing`.
`ArchivedShrinkage` and sold-with-comp passed on the old code too; they are
regression guards. All pass on the branch.

## Gate
`gofmt -l .` is clean. `go build ./...` and `go vet` on the touched packages
pass. `go test ./...` passes in every package except `internal/plugins`,
which hit the default 10-minute local timeout. That is the slow WASM suite;
it is untouched by this diff, and CI runs it. `golangci-lint run ./...`
reports 0 issues. These build-job
guards pass: data-access, price-history-sync, i18n, help-drift, help-topics,
docs-shots and the rest of the build job's guards. Two did not run locally:
`guard-deadcode-baseline` (the local `deadcode` binary is built with Go 1.26,
and it fails identically on `main`) and `guard-shellcheck-version`
(shellcheck is not installed). CI runs both.

## Not verified
No driven UI run: there is no new surface. The change is visible only as
my.'s Delete succeeding, plus the audit line.

Verdict: safe to merge once CI is green.
