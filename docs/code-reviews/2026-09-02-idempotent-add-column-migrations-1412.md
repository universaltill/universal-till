# Code review — idempotent `ADD COLUMN` in the migration runner + visible start failure on Android (ut-docs#1412)

**Date:** 2026-09-02 · **Branch:** `fix/1412-idempotent-add-column` · **Card:** ut-docs#1412 (P1, `complexity:medium`)

## What shipped

- `internal/db/db.go`: `applyMigration` now runs a migration **statement by
  statement** inside its transaction (`execMigrationStatements`): `--` comments
  are stripped (outside single-quoted literals), the text is split on semicolons
  outside literals, and each `ALTER TABLE [schema.]<t> ADD [COLUMN] <c>` is
  checked against `pragma_table_info` *immediately before it would run*; one
  whose column already exists is skipped with a boot-log warning, everything
  else executes unchanged. Matching is done on a literal-masked copy of the
  statement so quoted text can never match. A test seam records every skip.
- `internal/db/add_column_replay_test.go`: `TestMigrations_AddColumnReplaySafe`
  (fully migrated DB, ledger rewound to 77, reopen must succeed and record 78
  once — this also replay-tests every future ADD COLUMN migration),
  `TestMigrations_FreshInstallSkipsNothing` (a fresh install skips nothing and
  every shipped migration round-trips the strip/split losslessly), and
  `TestExecMigrationStatements_EdgeCases` (the review's findings 1–6 below).
- `android/.../MainActivity.kt`: the start-failure branch makes the status view
  visible before writing the error, so a release build no longer shows a blank
  WebView on a boot failure.

## Why

The TECLAST test tablet upgraded to v0.9.0 and showed a white screen. The Go
server died at boot with `exec migration 78 (078_sale_line_order_type.sql):
duplicate column name: order_type` — the device had run a pre-merge build of
ut-docs#1181 in which that migration carried a different number; after the
merge with #1390 it became 078, so a database at ledger 77 re-ran it. SQLite has
no `ADD COLUMN IF NOT EXISTS`; 077's own header records the convention that every
migration must be re-runnable, so the idempotence belongs in the runner, not in
an edit to the (append-only) 078 file.

## Verified beyond the automated tests

- TDD red/green: with the runner change reverted and a pass-through stub for the
  helper, `TestMigrations_AddColumnReplaySafe` fails with exactly the production
  error text; with the change restored it passes.
- `go test ./internal/db/... ./internal/app/... ./mobile/...` green; full
  `go test ./...` green except `internal/server`'s
  `TestListenWithFallback_WildcardHostFallsBackToLoopback`, which fails
  identically on `origin/main` on macOS while Linux CI is green — pre-existing,
  environment-specific, filed as ut-docs#1413.
- Android: `./gradlew :app:compileReleaseKotlin` exit 0.
- Not a UI-surface change in the product's own pages (no help-topic impact); no
  shop names or credentials in test data.

## Independent review (Opus, isolated worktree)

Reviewer re-ran build/vet/tests, re-verified the TDD claim personally (revert +
stub → the exact production error; restore → pass), and wrote its own scratch
tests. Verdict on the first draft: **SAFE TO MERGE, no blockers**, with these
findings — all acted on before the commit, since 1–3 were paths where a runner
could turn a loud failure into silent schema loss:

| # | Severity | Finding (first draft: whole-text regex rewrite) | Resolution |
|---|---|---|---|
| 1 | should-fix | `;` inside a quoted DEFAULT truncated the excision, leaving syntax garbage on replay | **Fixed** — literal-aware statement splitting; covered by `EdgeCases` (1) |
| 2 | should-fix | DDL-shaped text inside a string literal was matched and silently blanked | **Fixed** — matching runs on a literal-masked statement; `EdgeCases` (2) asserts the row is stored verbatim |
| 3 | should-fix | Existence checked up front against the pre-migration schema, so rebuild-then-add (the 030 pattern) skipped the ADD and lost the column permanently — reviewer reproduced it | **Fixed** — per-statement execution checks the table as it is at that moment; `EdgeCases` (3) |
| 4 | should-fix | Final ADD COLUMN without a trailing `;` silently opted out of replay safety | **Fixed** — the splitter keeps an unterminated last statement; `EdgeCases` (4) |
| 5 | nit | Schema-qualified table names not matched | **Fixed** — optional qualifier in the matcher; `EdgeCases` (5) |
| 6 | nit | Android bar forced VISIBLE on failure never returned to GONE after a sticky-service restart succeeded, re-exposing the bind address (ut-docs#412) | **Fixed** — success branch restores the DEBUG-conditional visibility |
| 7 | nit | Hand-rolled `contains`/`indexOf` in the test file | **Fixed** — removed with the rewrite of the test file |
| 8 | nit | Doc comment claimed 064/065 carry `--` inside literals; they carry apostrophes inside comments | **Fixed** — comment corrected |

Reviewer also confirmed: fresh-install byte-identity across all 78 migrations,
comment stripping safe for the whole corpus, `pragma_table_info(?)` binding
works on modernc, 078's UPDATEs are idempotent when the column pre-exists
(including a partial replay where only one of the two tables has it), no import
cycle from `internal/logging`, neither recurring bug class (missing `MkdirAll`,
cwd-relative path) applies, and `status_failed` already has ar/fa/tr strings.

## Verdict

**Safe to merge.** Second-pass gates after the fixes: `go vet` clean,
`internal/db`, `internal/app`, `mobile`, `internal/data`, `internal/pages/...`
all green, release Kotlin compile exit 0.

## Deferred

- Both test Pis (192.168.1.163 / .167) were unreachable; whether they carry the
  same pre-merge schema is unverified — noted on ut-docs#1412. With the runner
  fix they self-heal on upgrade either way.
- ut-docs#1413 (macOS-only listen test).
