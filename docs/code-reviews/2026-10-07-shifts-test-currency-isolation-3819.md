# Review — shifts page test isolation: pin the process-global currency (ut-docs#3819)

**Date:** 2026-10-07 · **Lane:** cloud-54 · **Author model:** Sonnet (complexity:easy) · **Reviewer:** Opus 5.5, fresh context

## What shipped (test-only)

`TestShiftsPage_OpenShiftShowsCurrentAndHistory` failed under
`go test ./internal/pages -run 'Plugin|Page'` (rendered `€50.00`, expected
`£50.00`) and passed alone. `httpx.InitCurrency` is a process global. Handlers
under test (`/api/setup`, settings save/upsert) call it with EUR/USD, and the
shared deps helpers never reset it. In that subset the leaker was
`TestSetupWizardDE_HappyPathInstallsBasePluginSynchronously`.

- `internal/pages/main_test.go`: `pinCurrency(t, code)` sets the currency and
  resets it to the GBP baseline in `t.Cleanup` (the ut-docs#970 convention).
- `newShiftsPageTestDeps`, `newFullAuthDeps` and `newRealDBDeps` call
  `pinCurrency(t, "GBP")`. The shifts tests no longer depend on another test's
  state, and the setup/settings leakers are undone per test.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Some inline `InitCurrency("GBP")` cleanups (`setup_page_test.go:147`, `restoreCurrencyAfter`) now duplicate the helper's | Accepted: they do no harm |
| 2 | minor | `httpx.SetDefaultLocale` leaks the same way (`de-DE` shows up in shuffled runs) | Out of scope: added to follow-up ut-docs#3822 |
| 3 | nit | Every caller passes "GBP" | Accepted: the parameter is kept for other currencies |

The reviewer checked several risks and found none:
- Cleanup ordering: cleanups run LIFO, and every reset goes to GBP.
- No test sets a non-GBP currency before calling one of these helpers.
- No test in the package uses `t.Parallel`.
- GBP really is the baseline: `ActiveCurrency()` falls back to it when unset.

## Verified

- TDD re-verified by the reviewer in a separate worktree. With the fix reverted, the
  `Plugin|Page` subset fails on the shifts test. With the fix restored, it passes.
- `go test ./internal/pages -count=1` (full package): ok. `-run 'Shift|Setup|Settings'`: ok.
- `go vet`, `gofmt -l`: clean.
- Shuffle seed 1 on the reviewer's subset: one order-dependent failure removed, none added.
  The remaining failures come from other leaking globals and predate this change (ut-docs#3822).

## Verdict

Safe to merge.
