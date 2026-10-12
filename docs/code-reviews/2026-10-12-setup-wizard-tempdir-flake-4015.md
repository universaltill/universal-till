# Review: TestSetupWizard* TempDir cleanup flake (ut-docs#4015)

Branch `fix/4015-setup-wizard-tempdir-flake`. Author: Opus 5.5
(lane:cloud-41, complexity:medium). Independent review: Fable, fresh
context, in a separate worktree.

## What happened

- `go test ./internal/pages -run SetupWizard -count=1` failed about 4 runs
  in 8 on a dev Mac with `TempDir RemoveAll cleanup: ... directory not
  empty` (found by the #4006 review).
- It did not reproduce on Linux (13 serial runs, 24 runs 8-way parallel on
  4 CPUs). The writer was found by reading the paths instead.

## Root cause

- Since ut-docs#3992, the setup wizard (`setup_page.go`), settings and
  sync-settings country changes call
  `startCustomerPhoneE164BackfillForCountryChange`. It is a goroutine on
  `d.AsyncWork` that queries `d.Db` after the handler has responded.
- The test helpers that wire those handlers never drained `AsyncWork`. The
  back-fill ran after the helper's `db.Close` cleanup. Evidence: `-v` logs
  showed `back-fill customers phone_e164 after country change: ... sql:
  database is closed` during the SetupWizard run.
- `newRealDBDeps` opens its DB through `db.Open` with WAL, so it keeps real
  `-wal`/`-shm` sidecars. A query in flight at `Close` can create or remove
  them while `t.TempDir`'s `RemoveAll` runs, which gives the Mac error. The
  `openPagesTestDB` helpers use `journal_mode=MEMORY`, so for them it was
  closed-DB noise.

## What shipped (test-only)

Same cleanup-ordering fix as ut-docs#514. Cleanups run LIFO, so
`t.Cleanup(d.WaitForAsyncWork)` registered after the DB-close cleanup runs
first:

- `newFullAuthDeps` (`setup_page_test.go`)
- `newRealDBDeps` (`demo_seed_opt_in_test.go`)
- `newAuthTestMux` (`auth_page_test.go`)
- `newSyncSettingsTestDeps` (`sync_settings_test.go`). A full-package `-v`
  scan found the back-fill outliving `TestSyncSettingsApply_*` too.

All four are pinned in `TestTestDepsHelpers_DrainAsyncWorkBeforeClosingDB`
(`async_cleanup_ordering_test.go`). Its header now names the back-fill.

## TDD

- The four new table rows failed before the helper fix ("hit a closed DB
  during cleanup"). The author saw this, and the reviewer re-verified it
  independently in its worktree. The five original helpers kept passing as
  the control.
- With the fix, all 9 sub-tests pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The regression test's header named only `completeTender`'s print goroutines. | Fixed: it names the #3992 back-fill. |
| 2 | nit | `WaitForAsyncWork` is unbounded. A test leaking `*sql.Rows` on a `MaxOpenConns(1)` DB would hold the drain for up to the back-fill's 2-minute ctx. | Accepted: no test does this, and it is ctx-bounded, so a slow test, not a hang. |

The reviewer looked for other late writers on the wizard path
(`installBasePluginsForSetup`, auto-register, TSE kickoff, layout sync,
plugin reload) and found none. They are synchronous with request-scoped
timeouts. The `Start*` retry loops are wired only by `pages.Init`. Helpers
without a drain (`newCloudSyncTestDeps`, `newMigratedSyncDeps`,
`newBasePluginTestDeps`, `newTSEReadyDeps`) have no test that changes the
country.

## Verification

- AC: `go test ./internal/pages -run SetupWizard -count=1` passed 20
  consecutive runs. After the fix, `-v` shows no back-fill "database is
  closed" line.
- Reviewer: `-race -run 'SetupWizard|TestTestDepsHelpers_…' -count=2` is
  clean, with 6 more SetupWizard runs.
- `gofmt -l .`: no output. `go build ./...`, `go vet`: clean.
  `golangci-lint run ./...`: 0 issues. The data-access, core-neutral,
  kiosk-engine, i18n, netaccess, card-data, page-http-error and
  deadcode-growth guards pass. `guard-deadcode-baseline.sh` can't run
  locally: the container's `deadcode` binary is built with go1.26 and the
  repo needs go1.27. It runs in CI's desktop job, and a test-only diff
  can't change it.
- Full `go test ./...`: green.
- Not verified: a Mac run. The card's environment wasn't available. The
  fix removes the only detached writer on the path.

## Verdict

Safe to merge.
