# Review: test helpers drain AsyncWork before closing the DB (ut-docs#4167)

**Bug.** `TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry`
failed 3 runs in 10 on main. The wizard's country save starts a background
phone_e164 back-fill on `d.AsyncWork`. `newRealDBDeps` closed the DB (and
its TempDir) without draining that work first, giving "sql: database is
closed" and "TempDir RemoveAll: directory not empty".

**Fix.** `t.Cleanup(d.WaitForAsyncWork)` in `newRealDBDeps`. Cleanups run
last-in-first-out, so the drain runs before `dbo.Close`; this is the same
fix ut-docs#514 made for the other helpers.

**Reviewer.** Independent Fable subagent: **approve**. The LIFO order is
right, and nothing can hang: the base-plugin retry worker uses its own
WaitGroup, and the back-fill has a 2-minute timeout. Its medium finding:
`newFullAuthDeps` and `newSyncSettingsTestDeps` had the same latent race.
**Fixed in this PR**, and all three helpers were added to
`TestTestDepsHelpers_DrainAsyncWorkBeforeClosingDB`. Low (noted, no
change): a test calling `initTestPaths` after the helper tears paths down
before the drain; that is harmless today because the back-fill touches the
DB only.

**Verified.** The flaky test: 0 failures in 40 runs, plus 40 under
`-race` (3 in 10 before). Removing the new drain from
`newSyncSettingsTestDeps` fails the ordering test. The full `internal/pages`
suite is green.

Verdict: safe to merge. Test-only change.
