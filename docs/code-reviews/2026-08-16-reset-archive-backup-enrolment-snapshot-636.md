# Review: reset-archive batches in backup + join/enrolment snapshot (ut-docs#636)

**Card:** universaltill/ut-docs#636 — "Reset-transaction archive batches
must be included in backup/enrolment snapshots"
**Complexity:** medium. **Build:** Sonnet (inline). **Review:** Opus
(fresh-context subagent, independent of the build).

## What was asked

ut-docs#187 turned `reset-transactions` from destructive delete into
archive+restore (`internal/db/migrations/040_reset_archive.sql`) — the
archived rows are retained fiscal data, not disposable test data. #636 asked
to verify (and fix if needed) that:

1. The till's local backup (Settings → Backup) includes the new archive
   tables.
2. The join/enrolment snapshot a joining replica receives does not silently
   drop them — the same "join-path gap class" ut-docs#368 found for plugin
   binaries (present in the DB copy, absent from disk).

## Investigation

- `Snapshot()` (`internal/db/backup.go`) — the local backup mechanism — is
  one statement, `db.Exec("VACUUM INTO ?", dest)`. `VACUUM INTO` rebuilds
  the **entire logical database** (every table in `sqlite_schema`); there is
  no table allowlist/denylist anywhere in the path.
- `RedactedJoinSnapshot()` (`internal/db/join_snapshot.go`) — used for
  `GET /api/sync/snapshot`, the join/enrolment snapshot — takes a raw byte
  copy (`io.Copy`) of `Snapshot()`'s output file, then runs exactly one
  `UPDATE tills SET bearer_hash = NULL` on the copy. No filtering beyond
  that single column/table.
- Traced both ends of the join path: server serves the file whole
  (`internal/pages/sync_api.go`), client stages it whole
  (`internal/db/replica.go`'s `StageRestoreFromReader`, a bare `io.Copy`),
  and applies it whole (`ApplyPendingRestore`, an `os.Rename`). The file is
  byte-identical from primary to replica disk.
- `db.Snapshot` has exactly two callers in the repo (Settings → Backup and
  the join snapshot) — no third, selective "backup" mechanism exists.
- The *ongoing* incremental replication allowlist (`adminTables` in
  `internal/data/sync_admin_repo.go`) does **not** carry the archive
  tables — correctly out of scope: it doesn't carry `sales` either, and a
  reset-archive batch is the primary's own record of its own reset, not
  data due to sync to a peer on an ongoing basis.

**Conclusion: both questions resolve negative — no gap exists.** Both
mechanisms already copy the whole database file rather than selecting
tables, so the reset-archive tables are carried automatically, with no
production-code fix required.

## What shipped

- Two regression tests pinning the finding, so a future rewrite of either
  copy step toward a selective/per-table export can't silently drop
  retained fiscal data again:
  - `TestSnapshotAndRestore_PreservesResetArchiveBatch`
    (`internal/db/backup_test.go`) — full round-trip: seed a
    `reset_batches`/`sales_archive` row, snapshot, mutate the live DB,
    stage+apply restore, confirm the archived (not the mutated) state comes
    back.
  - `TestRedactedJoinSnapshot_PreservesResetArchiveBatch`
    (`internal/db/join_snapshot_test.go`) — seed the same rows, call
    `RedactedJoinSnapshot`, read the served copy on a fresh connection,
    confirm both rows are present.
- A one-line load-bearing-invariant comment at the `VACUUM INTO` call site
  (`backup.go`) and at the `io.Copy` call site (`join_snapshot.go`), each
  referencing ut-docs#636, so a future author narrowing either copy sees the
  constraint at the point of change.
- A short "Reset-archive batches" section added to
  `ut-docs/architecture/local-backup.md` recording the finding and pointing
  at the two tests.

## Independent review (Opus, fresh context)

Verdict: **core factual claim confirmed independently** (read both files
directly, traced both ends of the join path); **tests are real, not
false-pass** — verified by temporarily mutating `Snapshot()` to `DROP`/
`DELETE` the archive tables on the copy and confirming both new tests fail
with the expected symptom, then reverting (working tree confirmed clean
afterwards, `go test ./internal/db/...` green); **SQL in the new tests is
schema-correct** against `040_reset_archive.sql` (all NOT NULL columns
supplied, FK insert-order correct, no CHECK constraint on `sales_archive`
to violate); test isolation/hygiene consistent with sibling tests in the
same files; doc addition has no ADR-0040 compliance-wording issue.

Two nits raised and fixed in this pass:
- Doc/comment wording said `VACUUM INTO` "copies" the file — it's a logical
  rebuild, not a byte copy (the byte copy is the later `io.Copy` in the join
  path). Corrected in both the doc and the test comment; the conclusion was
  unaffected.
- `backup_test.go`'s round-trip test opened the snapshot artifact via
  `Open(snap)` (runs migrations, flips journal mode) before restoring from
  it. Switched to a plain `sql.Open` read, matching the join test's
  approach, so the test reads the disaster-recovery artifact without
  mutating it first.

One suggestion evaluated and declined: a runtime table-presence guard in
`Snapshot()`/`RedactedJoinSnapshot()`. Rejected because `VACUUM INTO` has no
mechanism to omit a table — such a guard could only fire if SQLite itself
were broken — and it would cost a full-DB read on every backup and every
replica join, on the offline-first checkout path, against a null risk. The
real risk (a future selective-export rewrite) is a change-detection
problem, which the two tests already cover and were proven (by mutation) to
catch.

Two further observations noted as genuinely optional, not applied in this
pass: (1) `web/help/en/backups.md`'s "all your shop data" summary could get
one sentence confirming archived batches are included, since no user-visible
behaviour changed here the "manual ships with the feature" rule isn't
triggered; (2) coverage is 2 of 11 archive tables, proportionate given the
mechanism is table-agnostic (not exhaustive, and doesn't need to be to prove
the mechanism).

## Verification beyond automated tests

- `go build ./...`, `go test ./internal/db/... -race` (full package, all
  tests including the two new ones) — green.
- `go vet ./internal/db/...`, `gofmt -l` on every changed file — clean.
  (Repo-wide `gofmt -l .` shows unrelated pre-existing drift in 9 other
  files, tracked separately as ut-docs#779 — none of the files touched
  here.)
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — pass.
- A full `go test ./... -race` run separately hit a slow/flaky
  `internal/plugins` test (`TestHostTCPReadDoesNotOutliveEventDeadline`,
  timed out at 600s under system load) — confirmed unrelated: the test
  passes in isolation in ~25s, and this package is untouched by this diff.
  Consistent with the existing timing-margin class already tracked in
  ut-docs#648/#778.

## Safe-to-merge verdict

**Yes.** No blocker-class issues found or introduced. Test-and-doc-only
change; both review nits fixed in this same pass.
