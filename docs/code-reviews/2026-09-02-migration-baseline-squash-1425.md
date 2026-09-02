# Code review: squash 78 migrations into baseline 001_init.sql (ut-docs#1425)

## What shipped

Sub-card 1 of 4 of the ut-docs#1414 epic, design fixed by ADR-0074
(`ut-docs/adr/0074-migration-and-release-history-reset-before-first-paying-shop.md`,
landed this same cycle via ut-docs#1424 / universal-till#725). No real shop
has ever run this product, so `internal/db/migrations/`'s 78 append-only
files existed only to protect upgrade paths nobody has actually taken.

- New `internal/db/migrations/001_init.sql` is a dump from a fresh `Open()`
  run against the previous 78-migration ledger: full schema (`sqlite_master`
  creation order) plus every seed row the old ledger produced. Equivalence
  between the old sequence and the new baseline was proven by a temporary
  test (`internal/db/zz_baseline_gen_test.go`), run once before the old
  files were deleted — see "Equivalence evidence" below.
- `002_*.sql`..`078_*.sql` (77 files) deleted; `001_init.sql` is now the
  only migration file.
- New `schema_lineage` table + marker row, and a hard-fail guard in
  `db.go`'s `migrate()`/`checkSchemaLineage()`: a database carrying
  `schema_migrations` rows but no lineage marker predates the reset and is
  refused with the plain, actionable message ADR-0074 specifies, rather
  than silently under-migrated (the pre-fix code would have skipped the
  sole remaining migration, version 1, against any pre-reset watermark).
- `schema_migrations` gains `name`/`checksum` columns; `verifyAppliedMigrations`
  compares every already-applied version's recorded name/checksum against
  the on-disk file on every boot and hard-fails on a mismatch, unless the
  version is in the (currently empty) `idempotentRerunVersions` allowlist,
  in which case it re-applies in place via `reapplyMigration`.
- Deleted the migration-replay tests whose only job was proving the deleted
  78-step sequence composes; kept every test asserting live, final-state
  behaviour.
- `docs/data-model.md` documents the baseline, ledger columns and lineage
  guard. `internal/pages/setup_page_test.go`'s `country_settings` fixture
  now reads the real baseline via the new (test-support-only)
  `db.BaselineStatementsFor` helper instead of the two now-deleted
  migration files.

## Equivalence evidence (ADR-0074 Decision 2's acceptance requirement)

Captured by the Dev phase before the old migration files were deleted —
verbatim output of `go test ./internal/db/ -run TestZZGenerateBaseline
-count=1 -v` against the then-current 78-migration codebase:

```
=== RUN   TestZZGenerateBaseline
    zz_baseline_gen_test.go:330: PASS baseline equivalence: ledger max version=78; 75 tables + 64 indexes identical (sqlite_master, 139 entries); seed rows matched per table: settings=2 tax_codes=3 stock_locations=3 users=2 payment_methods=3 roles=4 permission_actions=19 role_permissions=54 country_settings=14; empty tables: 66; CURRENT_TIMESTAMP-defaulted columns left to default: settings.updated_at; candidate bytes=44979
    zz_baseline_gen_test.go:337: candidate written to <scratch path>/001_init.sql
--- PASS: TestZZGenerateBaseline (0.20s)
PASS
ok  	github.com/universaltill/universal-till/internal/db	0.199s
```

The comparison also asserted `pragma_foreign_key_check` empty on the
rebuilt candidate database and the `schema_lineage` marker present. The
generator test was then deleted along with the old migration files it
read (it can never run again once they're gone) — this record is the
acceptance evidence ADR-0074 requires in its place.

One deliberate deviation from "dump whatever's there": `settings.updated_at`
(`DEFAULT CURRENT_TIMESTAMP`) was left to its default rather than baked in
as a literal timestamp — baking it would freeze the generation date into
every future fresh install. `country_settings.updated_at` (an explicit
`'1970-01-01T00:00:00Z'` literal in the old `041`) was kept verbatim, since
that value is genuinely part of the seed, not a generation artifact.

## Review

Independent review by a different-model subagent (Opus; Dev ran on Fable —
`complexity:hard` routing), fresh context, no prior reasoning seen. Verdict:
**no blocking code defects**, 9 findings (all non-blocking/nits), all
addressed below.

### Findings and disposition

- **F1 (addressed)** — the ADR-mandated equivalence evidence didn't yet
  exist as a written record (both `001_init.sql`'s header and the commit
  message pointed at "the review record", which didn't exist yet at review
  time). Fixed by this document — see "Equivalence evidence" above.
- **F2 (fixed)** — `verifyAppliedMigrations` only walked on-disk files,
  never the ledger, so it caught a file renumbered *downward* under an
  applied version but missed the mirror case: a ledger row whose file was
  renumbered *upward* out from under it. Concretely, a device with 001–003
  applied where a later release renumbers `003_foo.sql` to `004_foo.sql`:
  001/002 verify clean, 004 is above the watermark so it's applied as if
  new, and ledger row 3 is never inspected — no drift reported, and
  depending on the migration's content this ranges from duplicated seed
  rows to a confusing boot failure to a silently orphaned ledger row.
  Fixed with a second pass in `verifyAppliedMigrations`
  (`internal/db/db.go`): after the existing files-first loop, every ledger
  row `<= current` is checked against the loaded migration set, and an
  orphaned row (no matching on-disk version) now hard-fails with a message
  naming the version and its recorded name. New test
  `TestVerifyAppliedMigrations_DetectsUpwardRenumbering`
  (`internal/db/migration_drift_test.go`). Fixing this exposed that the
  existing drift tests called `verifyAppliedMigrations` with an
  intentionally partial migration slice (just the one synthetic migration
  under test) — correct under the old files-only semantics, but the new
  reverse check needs the *complete* on-disk set (matching how `migrate()`
  actually calls it) or it misreads the real embedded baseline itself as
  orphaned. Added a `withBaseline(t, extra...)` test helper and updated
  every existing `verifyAppliedMigrations` call site to use it; re-verified
  all of `internal/db` green under `-race` afterward.
- **F3 (fixed)** — the squash deleted migration 040's header, which was
  the only place stating the "every `*_archive` table stays column-identical
  to its live twin plus `reset_batch_id`" invariant that
  `internal/data/reset_test.go` points readers at. Restated (condensed from
  the original) directly above the archive table block in `001_init.sql`.
- **F4 (fixed)** — a real coverage regression: the deleted
  `TestMigration038MatchesSeedData` was the only test cross-checking
  `seeddata.DemoCustomerIDs`/`DemoPromoCodes` against
  `DemoCustomersPromosIDsSQL`/`DemoCustomersPromosSQL` in both directions;
  the item-side equivalent (`TestDemoSeedItemsPristineValuesMatchCatalogue`)
  was correctly kept, making the split inconsistent. A demo customer added
  to `demo_customers_promos.sql` without also adding it to
  `seeddata.DemoCustomerIDs` would have seeded but become permanently
  un-removable. Added `TestDemoCustomersPromosIDsMatchSeedData`
  (`internal/db/demo_seed_test.go`), mirroring the item-side test's
  both-directions shape (forward containment + a row-count check in the
  reverse direction) without depending on the deleted migration files.
- **F5 (fixed)** — `001_init.sql`'s header claimed FOREIGN KEY targets
  always precede their referrers; six tables actually reference `customers`
  or `tables` before either is defined (SQLite resolves FK targets at DML
  time, not DDL time, so this is harmless — confirmed by the full green
  suite — but the stated invariant was false and could mislead a future
  editor reordering the file). Corrected the header comment to say so
  explicitly rather than reordering 1200+ proven-equivalent lines.
- **F6 (fixed)** — `dead_seed_test.go` was deleted whole, but
  `TestDeadTaxInclusiveSeedRemoved` was pure final-state (no rewind) and
  should have been kept per the change's own stated rule. Restored as
  `TestBaselineSeedsNoDeadTaxInclusiveKey`
  (`internal/db/demo_seed_test.go`) — the baseline is currently correct,
  but since ADR-0074 makes `001_init.sql` freely editable pre-revenue,
  nothing else would catch the dead key being re-added by a future edit.
- **F7 (fixed)** — five comments elsewhere in the tree instructed a reader
  to consult a specific now-deleted migration file to "keep in sync":
  `internal/auth/auth_test.go`, `internal/data/sync_quarantine_repo.go`,
  `internal/data/demo_seed_repo.go`, `internal/pages/ui_smoke_test.go`
  (the `table_claims` fixture comment), `internal/pages/pos_status_test.go`
  (the `vouchers`/`voucher_transactions` fixture comment). All repointed at
  the real table in `001_init.sql`, noting the migration each originally
  shipped in for historical context. Several other mentions of specific
  migration numbers in the tree are genuinely historical provenance notes
  (e.g. "with the corrected EAN-13 check digits from migrations 023 and
  031 already applied") rather than live "go check that file" pointers —
  ADR-0074 explicitly exempts those, left unchanged.
- **F8 (accepted, not fixed)** — `migrationChecksum` is sensitive to
  reindentation/reformatting of an already-applied migration, which is
  stricter than strictly necessary now that `001_init.sql` is freely
  editable pre-revenue. Reviewer's own framing: "defensible by design."
  Not fixing — a reformat of an applied file is rare enough, and the
  actionable failure message (with `git diff` right there) makes the false
  positive cheap to diagnose, that adding fuzzier normalization isn't worth
  the risk of *under*-detecting real drift.
- **F9 (accepted, not fixed)** — `migration_drift_test.go` mutates the
  package-level `idempotentRerunVersions` map with `defer delete`; safe
  today (confirmed no `t.Parallel()` anywhere in `internal/db`, `-race`
  clean), becomes a real race only if that changes. Left as-is; whoever
  adds the first `t.Parallel()` to this package should revisit it.

### TDD verification — independently re-run by me

- Reverted `internal/db/db.go` only (`git stash push -- internal/db/db.go`):
  the new test files (`lineage_test.go`, `migration_drift_test.go`,
  `baseline_statements_test.go`) fail to *compile* (undefined
  `ErrDatabasePredatesReset`, `verifyAppliedMigrations`, `migrationChecksum`,
  `BaselineStatementsFor`) — confirms they exercise genuinely new
  production code, not a tautology. Restored, build green again.
- F2's fix: ran the full `internal/db` suite before the fix (one real
  failure surfaced by my own new test, `TestVerifyAppliedMigrations_DetectsRenameAndEdit`
  — its "pending"/"never" subtests broke because the new reverse check
  needs the complete migration set, not the partial slice those calls had
  been passing), fixed the test call sites, reran — green.

### Full gate — re-run and confirmed by me after applying every fix above

```
gofmt -l .                                          → empty
go build ./...                                       → clean
go test ./... -count=1                                → all 44 packages ok, zero FAIL
go test ./internal/db/... -race -count=1              → ok, 29.5s
bash scripts/ci/guard-data-access.sh                  → pass
bash scripts/ci/guard-migration-version-collision.sh  → pass
bash scripts/ci/guard-i18n.sh                         → pass
```
Plus, earlier in the same session before the F2–F7 fixes: all 18
CI-blocking guards from `.github/workflows/ci.yml`'s `build` job (the three
above re-run again after the fixes; the rest are untouched by anything
these fixes changed — no i18n strings, no compliance wording, no kiosk
routes, no plugin-menu-read surface).

No SQL text added anywhere outside `internal/db`/`internal/data`
(`guard-data-access.sh` passes — `setup_page_test.go` *removes* a
migration-file read, doesn't add one elsewhere). No money-type
involvement. No new user-facing strings (`ErrDatabasePredatesReset` is a
pre-i18n boot error, correctly not keyed).

## Safe-to-merge verdict

**Yes.** No blocker-class finding from the independent review. Every
non-blocking finding is either fixed (F1–F7) or explicitly accepted with
reasoning (F8, F9). Full gate green, independently re-verified.
