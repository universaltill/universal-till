# Review — internal/pages shuffle isolation: reset every httpx process-global per test (ut-docs#3822)

**Date:** 2026-10-07 · **Lane:** cloud-24 · **Author model:** Opus 5.5 (complexity:medium) · **Reviewer:** Fable, fresh context, own worktree

## What shipped (test support only)

`go test ./internal/pages -shuffle=on` failed 2–4 tests on every seed tried.
The cause was the same as #3819's currency leak, but wider. Handlers under test
publish settings into `internal/httpx` process globals, and nothing undid them.
Observed leaks: the default locale (`lang="tr"`, Arabic copy), a translator
replaced by `nil` (raw keys such as `basket.table.occupied`), UI scale 1.5, OSK
mode, and the `CrossDeviceLinkActionable` platform seam.

- `internal/httpx/state_snapshot.go`: `SnapshotStateForTests()` captures every value
  the package publishes and returns a reusable restore func. That covers the
  translator, default locale, currency, UI scale, OSK, effects, order-type
  prompt, idle lock, locale generation, kiosk, self-order, display mode, theme,
  both rail sources and the three platform-seam func vars. A value never set
  is restored as its type's zero value, which every getter already reads as
  its default. Test support only; production never calls it.
- `internal/pages/main_test.go`: `TestMain` takes the baseline right after it
  wires real i18n. `resetProcessGlobals(t)` restores the baseline at once and
  again in `t.Cleanup`.
- `chdirRoot` (the first call of nearly every handler test and shared deps
  helper) calls `resetProcessGlobals`. So a test starts from the baseline,
  whatever a helper-less test before it left behind. `openPagesTestDB` adds
  only the exit reset, because callers set test globals between the two calls.
- `demo_mode_test.go`: the two tests that wire a `nil` translator now reset
  (this was the leak behind `TestTableHandler_RejectsTableOfParkedOrder`).
  `auth_page_test.go`: one test set its UI scale before the helper. It now sets
  it after.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The seam assertion stubbed every seam to `true`. On windows/darwin `DownloadLinkActionableNow()` is already true, so a dropped seam restore would pass there | Fixed: each stub returns the opposite of the baseline; the mutation check fails on linux with the restore removed |
| 2 | minor | `SnapshotStateForTests` lists the globals by hand, so a new `Init*` global would escape it silently | Fixed (comment): the `var (` block in `httpx.go` now says every published value must be added to `SnapshotStateForTests`. A grep guard is not worth its code for this |
| 3 | nit | A few tests set a global before a helper that now calls `chdirRoot` (`money_prefill_locale_2818_test.go`, `rederive_cached_settings_test.go`, `export_save_notice_test.go`, `till_role_test.go`, `open_orders_page_test.go`), so those pins are now dead code | Accepted: every one sets exactly the baseline value, so each test still tests what it claims |
| 4 | nit | Restore puts back which translator is wired, not that translator's overlay state | Fixed (doc comment). No current test installs an overlay into the shared translator |
| 5 | CI | `guard-deadcode-baseline` (desktop-shell job) flagged `SnapshotState` and `restoreValue`: they are reachable only from `_test.go`, which `deadcode -test=false` cannot see | Fixed: renamed to `SnapshotStateForTests` (the `ResetCacheForTests` convention), inlined `restoreValue`, and added the one documented baseline entry the guard's header prescribes for test-only helpers. The local guard run no longer flags it |

The reviewer checked several risks and found none:
- Every mutable httpx global is covered. `i18nEpoch` is monotonic and comes back with the restored translator.
- The zero-value types are consistent, so `atomic.Value.Store` cannot panic.
- No test in the package uses `t.Parallel`. The new tests pass under `-race`.
- Cleanup order is LIFO and always ends at the baseline.
- Exporting a test-support func is acceptable: a different package needs it, and httpx already exports test seams.

## Verified

- Red first: before the change, the four seeds on the card fail 4, 3, 2 and 3
  tests on current `main`. These are different tests from the ones the card
  lists, because the package's test set has changed since the card was filed.
- After the change, the full `internal/pages` package passes in 9 orders:
  default, the card's four seeds, and seeds 11, 22, 33 and 44.
- An intermediate version reset on entry in `openPagesTestDB` as well. It
  failed the same 4 tests in every order, the default included, because those
  tests set a global between `chdirRoot` and `openPagesTestDB`. That is why
  only `chdirRoot` resets on entry.
- `TestSnapshotState_*` mutation check: with the UI-scale restore and the seam
  restore removed, the test fails on exactly those two values.
- The reviewer re-verified TDD in its own worktree. With only the `chdirRoot`
  hook reverted, seed `1791386697499295313` fails
  (`TestFiscalSignAsk_CannotSignRefusesCashTender`). Restored, it passes
  (3m03s).
- Clean: `gofmt -l`, `go vet`, `go build ./...`, `go test ./...` (every
  package; `internal/pages` 9× as above). Guards also pass: data-access,
  netaccess, i18n, kiosk-engine, page-http-error, pipefail-grep-q and
  core-neutral.
- Not run locally: `golangci-lint`. The container's binary was built with
  go1.25 and the repo targets 1.27.1, so CI's lint job is the check.

## Deferred

- A CI step that runs `internal/pages` with `-shuffle=on`. It is optional on the
  card and filed as a follow-up. A random seed in CI could turn `main` red on a
  leak this sweep did not hit, so it should land with a pinned seed list or an
  explicit owner call.

## Verdict

Safe to merge. No blockers, no majors; both minors are fixed.
