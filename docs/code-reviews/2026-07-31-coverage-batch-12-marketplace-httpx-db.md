# Code review — coverage batch 12: marketplace, httpx, db (2026-07-31)

**Branch:** `test/coverage-batch-12-marketplace-httpx-db` (ut-docs#9, batch 12)
**Scope:** test-only additions in three packages + one small production fix in `internal/httpx`.

## What shipped

- `internal/httpx/template_helpers_test.go` — render pipeline (NewRenderer/Render/RenderWith), JSON handler wrapper, template-helper family (minorUnits, toJSON, imgVersion, IsRTL, Currencies), init/clamp functions (UI scale, OSK mode, idle lock, kiosk), T/DefaultLocale/ResolveLocale fallbacks, mux dispatch.
- `internal/plugins/marketplace/client_more_test.go` — ResolveURL, DeviceIDFromConfig, AckDownload, ReportPluginStatus (opt-in posts, opt-out sends nothing, server error), GetRevocations, IssueDownloadToken error envelopes (each branch's distinct message pinned), GetOrFetch cache/fetch/offline, corrupt-snapshot handling, cache-dir failure.
- `internal/db/tx_test.go` + `backup_more_test.go` — WithTx commit/rollback/begin-error, StageRestoreFromReader staging + failed-copy cleanup, Open with uncreatable data dir, backup-dir error propagation, Snapshot on closed DB, StageRestore missing backup, ApplyReplicaIdentity no-file/corrupt-file.
- **Production fix:** `internal/httpx/httpx.go` — `T()` and the `FuncsFor` `"T"`/`"locales"` closures guarded with an interface nil-check that a typed-nil `*config.I18n` (stored by `InitI18n(nil, …)`) passes, then panicked on `mu.RLock` of a nil receiver. New `translator()` helper does a typed assertion. TDD: test written first, confirmed panicking pre-fix (nil pointer dereference at `config/i18n.go:69`), passing post-fix. No behavior change for real callers — the only production caller (`internal/pages/init.go`) always passes a non-nil translator; typed-nil now degrades to key-fallback instead of panicking.

## Coverage

| package | before | after |
|---|---|---|
| internal/httpx | 65.1% | 94.5% |
| internal/plugins/marketplace | 62.3% | 85.7% |
| internal/db | 66.7% | 79.8% |

Accepted remainder (documented, not theater): `db` migrate/applyMigration/loadMigrations error branches need fault injection into embedded migrations; Snapshot same-second reuse branch is timing-dependent; ApplyPendingRestore rename-failure branches need forced-IO mocking. `marketplace` doRequest/doWithFallback internals partially covered by existing dev-override tests. `httpx` remainder is assetVersion boot-time fallback and template-func plumbing exercised only via full renders.

## Verification beyond automated tests

- Full `go test ./...` green; `go vet` clean; `guard-data-access.sh` and `guard-i18n.sh` pass.
- Mutation spot-checks by Tester (all correctly failed their new test): WithTx ignoring fn error → `TestWithTxRollsBackOnError` FAIL; telemetry opt-out check removed → `TestReportPluginStatusOptOutSendsNothing` FAIL; staged-restore cleanup removed → `TestStageRestoreFromReaderCleansUpOnCopyError` FAIL.
- TDD claim re-verified by Reviewer personally: fix stashed → panic reproduced; restored → green.

## Independent review (different model: Opus subagent)

Ran build/vet/tests itself, including `-race` and 6× `-shuffle=on` runs on httpx. Independently re-verified the typed-nil fix by restoring the pre-fix code and watching the test panic. Verdict: nothing blocking; 2 should-fix + 5 nits, **all fixed**:

1. **Should-fix (proven false-pass):** the `"error envelope"` subcase of `TestIssueDownloadTokenErrorPaths` still passed with the error-envelope branch deleted (fell through to the missing-data error). Fixed by pinning each subcase's distinct error message; mutation re-run now FAILs correctly. Same message-pinning applied to the AckDownload/GetRevocations/ReportPluginStatus server-error tests.
2. **Should-fix (tautology):** `TestNewMux` only nil-checked a `http.NewServeMux()` wrapper — unfailable. Replaced with a real dispatch assertion.
3. Nits, all applied: length guard in the stripWebPrefixes comparison; `defer` restores for the uiScale/oskMode/idleLock package atomics; `realI18n` helper deduplicated into `httpx_test.go`; comment documenting that `StageRestoreFromReader`'s no-MkdirAll behavior is deliberate (staging happens next to an existing DB); removed redundant `io.Reader` interface assertion.

Review also confirmed: no real client/shop names, no credential-shaped literals, no leaked httptest servers, money/data-access/i18n rules respected, and neither recurring bug class (missing MkdirAll / cwd-relative path) applies to the production diff.

## Verdict

**Safe to merge.** Test-only change plus a strictly-defensive production fix, all gates green after review fixes.

## Deferred

- `internal/plugins/storage` (39.3%) stays excluded — dead-code decision is ut-docs#28.
- Next-lowest packages for batch 13 (from this batch's sweep): `internal/ai` 68.3%, `internal/paths` 69.7%, `internal/logging` 73.5%, `internal/pos` 74.4%.
