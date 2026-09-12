# ApplyAdmin: batch row upserts into chunked multi-row statements

**Card:** ut-docs#1369 · **Complexity:** hard · **Built by:** Sonnet (inline,
scrum-master `lane:cloud-24`) · **Reviewed by:** Opus (independent subagent,
isolated worktree)

## What shipped

`ApplyAdmin`'s phase-2 upsert path (`internal/data/sync_admin_repo.go`)
issued one `INSERT ... ON CONFLICT` statement per row of the *entire* admin
sync bundle on every replica's ~30s pull — because `DumpAdmin`'s own
change-marker (ut-docs#1368, merged) is a coarse whole-bundle generation
counter, not a per-row diff, one price edit anywhere still ships the whole
bundle, and every replica re-applied thousands of byte-identical rows as
individual statement executions.

Changed:
- Extracted the per-row column-resolution rules (skip/redact/missing-column
  handling) out of the old single-row `upsertRow` into a pure
  `resolveUpsertRow` helper.
- Added `buildUpsertBatches` (pure, DB-free): groups rows by their exact
  resolved column signature (guards against a rolling-upgrade primary whose
  dump has a different column set for the same table) and splits each group
  into `maxBatchPlaceholders`-safe chunks.
- Added `upsertRows`/`execUpsertBatch`: execute one chunked multi-row
  `INSERT ... VALUES (...),(...) ON CONFLICT` statement per chunk instead of
  one exec per row.
- Wired `upsertRows` into all three of ApplyAdmin's phase-2 write paths —
  the generic per-table loop, `applyPluginSettings`, and
  `applyFiscalRegisterStorage`.
- Removed the now-dead single-row `upsertRow` (logic preserved in
  `resolveUpsertRow`).
- `maxBatchPlaceholders = 4000`, chosen from a real measurement against this
  repo's driver (see Findings below).

No wire/bundle format change, no protocol change — write-batching only.

## Independent review

Spawned an Opus subagent (`general-purpose`, `isolation: "worktree"`) against
the WIP commit on `fix/1369-applyadmin-batch-upsert`. It built, vetted,
gofmt-checked, ran `golangci-lint`, ran the full `internal/data` suite, ran
the data-access guard, and independently redid the revert-then-restore TDD
check.

### Findings (all addressed)

1. **Should-fix — real.** `applyPluginSettings` and
   `applyFiscalRegisterStorage` were left calling the old per-row `upsertRow`
   instead of the new batched path. `applyFiscalRegisterStorage` was flagged
   as the sharpest case: a blanket `DELETE` followed by one `INSERT` per
   surviving row on every pull, over an unbounded fiscal-register keyspace —
   precisely the pathology the card was filed against. **Fixed**: both now
   build a filtered row slice and call `upsertRows` once.
2. **Should-fix — real.** No test asserted the actual statement-count
   reduction through the real `ApplyAdmin` path — the round-trip test alone
   would pass unchanged against the old per-row code (verified: reviewer ran
   it against `main` and it passed). **Fixed**: added
   `TestAdminApplyINSERTCountDoesNotGrowLinearlyWithRowCount`, generalizing
   `export_repo_querycount_test.go`'s SELECT-counting harness (ut-docs#229)
   to count INSERT statements. Personally re-verified against pristine
   `main`: adding 450 extra rows costs exactly 450 more INSERT statements
   under the old code (1:1, i.e. `growth == 450` against this test's
   `growth > 20` bound) — confirms the test is a real regression guard, not
   a no-op. Against the fixed code, growth is 0 (both sizes fit in the same
   handful of chunk statements for the tables this test touches).
3. **Nit — real, measured.** `maxBatchPlaceholders` was set to a
   conservative 500 without measuring the actual driver's real limit.
   Reviewer measured directly against `modernc.org/sqlite` v1.58.0: 32,764
   bound parameters in one statement succeeds, 40,000 fails ("too many SQL
   variables"). **Adjusted** to 4000 — a wide safety margin under the
   measured ceiling while capturing most of the real win (the widest
   `adminTable`, `items` at 19 columns, now chunks at ~210 rows instead of
   the original draft's 26).
4. **Nit — near-unreachable, no action taken.** Batches are emitted
   group-by-group, so a *mixed-signature* bundle (only possible during a
   rolling upgrade where two rows of the same table resolve to different
   column sets) doesn't preserve the bundle's original row interleaving
   across groups. Harmless in the normal uniform case; documented in
   `buildUpsertBatches`' doc comment and pinned by
   `TestBuildUpsertBatches_DifferingColumnSetsGetSeparateGroups`.

### False leads chased and cleared (by the reviewer)

Grouping-by-`names`-while-carrying-first-row's-`sets` (sound: `sets` is a
pure function of `t`, never of the row); placeholder/arg count mismatch
(none, appended in lockstep); SQL injection (column/table names come from
`tableColumns`'s live-schema read and the fixed `adminTables` literal, never
the wire); duplicate PKs inside one multi-row statement (SQLite applies a
multi-row upsert sequentially, last-write-wins — same outcome as the old
per-row code, verified empirically); divide-by-zero on a degenerate
`chunkRows` (guarded twice); `settings`/`country_settings` special-case
semantics — the per-till-setting skip and the ADR-0040 `archive_min_days`
floor clamp — (character-identical after being hoisted out of the write
loop into a pre-pass; `TestAdminApplyCountrySettings_ClampsArchiveMinDaysToGlobalFloor`
still passes); transaction/offline-first semantics (still one `tx`, full
rollback on any error, batching only changes failure granularity from
per-row to per-chunk, unobservable given the whole-transaction rollback);
money/tax truncation (values pass through untouched from the JSON wire trip,
no conversion added at this layer); the test's seeded-row-count math
(checked against migration 001's actual seed data, correct).

## Verified beyond automated tests

- `gofmt -l .` clean; `go build ./...` clean; `go vet ./...` clean.
- `golangci-lint run ./...` — 0 issues.
- `go test ./...` (full repo, no `-race`) — all packages green. (`internal/plugins -race` is a
  known pre-existing hang unrelated to this change, tracked separately as
  ut-docs#2156 — not run here.)
- `scripts/ci/guard-data-access.sh` — pass (all SQL stays inside
  `internal/data`).
- TDD re-verification (personally, in addition to the reviewer's own pass):
  reverted `sync_admin_repo.go` to pristine `main`, confirmed
  `sync_admin_batch_test.go`'s pure unit tests fail to build
  (`undefined: buildUpsertBatches`, `undefined: maxBatchPlaceholders`), and
  — isolating just the new INSERT-count test into its own file to work
  around that same build failure — confirmed it fails for real against `main`
  (`growth == 450`, i.e. exactly 1:1 with the extra row count). Restored the
  fix and confirmed all tests pass again, `git diff` clean.
- Not run: a live multi-till hardware/device test (no device available in
  this cloud sandbox) — this change is backend-only (`internal/data`), has
  no UI surface, and the existing sync test coverage (offline-first,
  conflict resolution, redaction, retire-in-place, role-permission skew)
  all pass unchanged through the new batched path.

## Explicitly out of scope / deferred

- A true per-row diff (the acceptance criteria's alternative option) —
  not pursued, since ut-docs#1368's landed design (a coarse whole-bundle
  generation counter) doesn't provide a per-row change signal to diff
  against; batching was the achievable option given that design, as the
  card's own "Complexity note" anticipated.
- Further widening `maxBatchPlaceholders` closer to the measured 32,764
  ceiling — 4000 is a deliberate, conservative choice with margin for a
  future driver/SQLite build shipping a lower compile-time limit; revisit
  if profiling shows it's still the dominant cost on a very large catalog.

## Safe-to-merge verdict

Safe to merge. No blockers found by the independent review; both should-fix
findings were addressed and re-verified; the nit was addressed with a
measured, documented constant; the remaining nit needs no action per the
reviewer's own assessment.
