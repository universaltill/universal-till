# 2026-09-19 — Diagnostic-mode follow-up fixes (ut-docs#2235)

## What shipped

Four independently-diagnosed defects found during the review of ut-docs#2169
(diagnostic mode, ADR-0092), grouped as one card since each is small and
none alone justified its own:

1. **Bounded `PendingSummary`'s per-render cost.** `GET /settings`
   unconditionally computed `diagnosticsViewFor` (`internal/pages/
   diagnostics_settings.go`), which calls `diagnostics.PendingSummary()`
   whenever a diagnostics session is active — unmarshalling every pending
   batch file on disk (up to ~40 MB worst case) — even for a cashier, whose
   `#settings-diagnostics` card is template-gated (`{{ if .isManager }}`)
   and never renders it. New `diagnosticsViewIfManager` (`internal/pages/
   diagnostics_settings.go`) short-circuits to the zero-value view when
   `!isManager`; `internal/pages/settings_page.go`'s data map now calls it
   instead of `diagnosticsViewFor` directly. The four dedicated diagnostics
   API handlers (activate/stop/cancel-stop/GET fragment) are untouched —
   already gated by `requireManager` inside their own handlers, and
   genuinely need the real view regardless of this page's `isManager` flag.
2. **Corrected `evictOverflow`'s doc comment.** It claimed strict global
   oldest-first eviction; `Pending()` actually sorts by `SessionID` (an
   opaque string) then `Seq`, which is only chronological within one
   session. During the narrow, self-limiting window where two sessions'
   on-disk batches coexist (around activate/revoke), eviction order across
   sessions isn't strictly chronological. No behavior change — comment-only,
   confirmed by diffing the function body.
3. **Closed the `Flush`/`Stop` race.** `Flush` read `current.Load()` at its
   top; if a concurrent `Stop` (`current.Swap(nil)` + `drainSessionDir`)
   interleaved between that read and `Flush`'s later `os.MkdirAll`+write,
   `Flush` could recreate the session's pending directory and write a fresh
   batch file *after* `Stop` had already deleted everything — resurrecting
   a batch `Stop`'s "discards every not-yet-uploaded batch" guarantee says
   is gone. New package-level `flushStopMu sync.Mutex`, taken at the very
   top of both `Flush` (`internal/diagnostics/queue.go`) and `Stop`
   (`internal/diagnostics/session.go`) and held for their entire body,
   fully serializes the two. `Revoke`'s non-matching-session
   `drainSessionDir` path deliberately does *not* take the new mutex — it
   never touches `current`, so it can't race `Flush`'s use of it (the one
   real adjacency, `evictOverflow`'s cross-session `os.Remove`, is already
   benign: `Pending()` skips unreadable files and `writeBatch` is
   temp+rename, so no partial file is ever visible either way).
4. **Wired Android's `Build.MODEL` into `Environment.DeviceModel`.** Was
   always `""` — the Android shell never plumbed it through the gomobile
   bind. New `internal/diagnostics/device_model.go`
   (`SetDeviceModel`/`DeviceModel`) mirrors `internal/bluetooth`'s
   `SetAndroidBridge`/`RegisteredAndroidBridge` shape exactly; `mobile.go`
   gains `SetDeviceModel(model string)` forwarding to it; `TillService.kt`
   calls `Mobile.setDeviceModel(Build.MODEL)` right before the existing
   `Mobile.setBluetoothBridge(...)` call, same `onCreate` worker-thread
   block, so the very first inventory event of a boot already carries it.
   `emitDiagnosticsInventory` now sets `DeviceModel: diagnostics.DeviceModel()`.

## Independent review

Opus, in an isolated worktree (`isolation: "worktree"`, no shared checkout
with the orchestrating session), reviewing a diff it did not write (Dev ran
at Sonnet, per this card's `complexity:medium` routing). Ran the full gate
for real: `gofmt -l .`, `go build ./...` (+ `./mobile/...` for the import
cycle check), `go vet ./...`, full-repo `go test ./...`, `golangci-lint run
./...` (0 issues), `guard-data-access.sh`, `guard-i18n.sh`,
`guard-kiosk-engine.sh`, `guard-page-http-error.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh`,
`guard-android-i18n.sh`, `guard-android-status-address.sh`,
`guard-android-external-links.sh`, `guard-android-manifest-features.sh`,
plus `-race` on the diagnostics/cloudsync/issuereport/pages subpackages —
all green except one blocker below.

**One blocker, fixed:** `scripts/ci/deadcode-baseline.txt` had no entry for
the new `diagnostics.SetDeviceModel` — its only production caller,
`mobile.SetDeviceModel`, lives in the gomobile-bind package `mobile`, which
has no `main` and so is invisible to `guard-deadcode-baseline.sh`'s
whole-program analysis (same shape already recorded for
`internal/bluetooth.SetAndroidBridge`, reached the identical way via
`mobile.SetBluetoothBridge`). Confirmed directly:
`go run golang.org/x/tools/cmd/deadcode@v0.48.0 -test=false .` reports
`internal/diagnostics/device_model.go:17:6: unreachable func: SetDeviceModel`.
**Fixed**: added the baseline entry (alphabetically sorted, between the
`internal/data` and `internal/diagnostics/events.go` lines) and a doc
comment on `SetDeviceModel` explaining the false positive, mirroring
`SetAndroidBridge`'s own comment, so a future baseline burn-down pass
(ut-docs#1566) doesn't delete it as if it were real dead code. Could not
run the guard itself in this environment — it needs the desktop build's
cgo GTK/WebKit dependency, and this sandbox's package mirrors were
unreachable when installing them; confirmed the identical failure occurs
on the pre-fix commit too, so it isn't something this diff caused. Real
verification is CI's `build` job, which the repo's own toolchain has.

**Two should-fix findings, both fixed:**

1. `TestSettingsPage_DiagnosticsPendingSummaryOnlyComputedForManager`'s doc
   comment claimed to "prove both halves" of the fix, but the review's own
   TDD re-verification showed it passes even with Fix 1 fully reverted —
   the rendered HTML can't distinguish "PendingSummary ran and the card is
   merely hidden" from "PendingSummary never ran," since the card is
   template-gated either way. The actual proof is the sibling
   `TestDiagnosticsViewIfManager`, which asserts on the returned Go value
   directly and does fail when reverted. **Fixed**: reworded the comment to
   describe this test as the outer page-level guard alongside the real
   proof, not a proof of the disk-scan skip itself — no test changes, only
   the doc comment was overclaiming.
2. `evictOverflow`'s new comment cross-referenced the `Flush`/`Stop` race
   fix as "the same tone... both windows are brief, both sides converge on
   their own" — but that race is now a closed correctness guarantee
   (`flushStopMu`), not an accepted inaccuracy, making the analogy stale on
   arrival and self-contradicting within the same commit. **Fixed**:
   reworded to state plainly why this one stays accepted (best-effort
   ordering nothing promises to be exact, unlike Stop's discard guarantee)
   and clarified that only the *eviction order* is inexact — evicted events
   are still gone for good, not recovered once the coexistence window
   passes.

**One nice-to-have, fixed:** `flushStopMu`'s own doc comment said `Stop`
runs "only from the settings stop handler and Revoke" — missed a third
caller, `internal/cloudsync/diagnostics.go`'s terminal-409/404
session-rejection path. Corrected.

## Verified beyond automated tests

- Independent TDD re-verification of Fix 3 (the highest-risk change): with
  only the `flushStopMu.Lock()/Unlock()` call sites removed (mutex `var`
  kept so the test file still compiles), `go test -race -run
  TestFlushStopMu_ConcurrentFlushAndStopNeverLeavesABatch -count=5` failed
  4 of 5 runs — reproducing the exact documented failure (a batch file
  resurrected after `drainSessionDir`), all within the first 4 of the
  test's 50 iterations. The sibling `TestFlushStopMu_BlocksFlushWhileStopSectionHeld`
  failed 3/3. Restored (`git checkout --`), re-ran both at `-count=10`
  under `-race`: clean. Fix 4's test was independently re-verified the same
  way (revert `DeviceModel: diagnostics.DeviceModel()` → the environment
  event's `device_model` assertion fails with the expected message).
- Mutex correctness read by hand: taken as the first statement of both
  `Flush` and `Stop`, held through each function's entire body (including
  `Flush`'s `evictOverflow()` and `Stop`'s `drainSessionDir`+settings
  writes), neither calls the other, no other caller in the codebase holds
  a lock that could deadlock against it (`cloudsync.Tick` has no mutexes of
  its own; its `uploadPendingDiagnostics` calls `Flush` then `Stop`
  sequentially, never nested), and `flushStopMu` is consistently the outer
  lock relative to `ring.mu` (nothing takes `ring.mu` and then calls
  `Flush`/`Stop`).
- Confirmed the recurring bug classes this pipeline keeps re-finding are
  absent: `Flush`'s `os.MkdirAll(dir, 0o755)` is still present and
  unaffected by the mutex refactor; no cwd-relative path was introduced
  (`diagnostics.PendingDir` is still sourced from `paths.Data(...)` in
  `internal/pages/init.go`, untouched).
- Confirmed `#settings-diagnostics` in `web/ui/pages/settings.html` is
  genuinely the only template consumer of the `"diagnostics"` data key
  (`diagnostics_chip.html` uses a separate `.canManage` key), and that all
  four dedicated diagnostics API handlers call `requireManager` as their
  first statement and still call the real `diagnosticsViewFor` — this
  change doesn't hide the feature from an actual manager anywhere.
- No real client/shop name or secret-shaped literal anywhere in the diff or
  its tests (test fixtures use `Pixel 8`, `sess-pending`, `sess-gate`, etc).
- Kotlin/Android side (`TillService.kt`, `mobile.go`'s gomobile-bind
  surface) could **not** be compile-verified in this environment — no
  Android SDK. Written to match the existing `Mobile.setBluetoothBridge`
  call's exact style, placement and ordering (verified the boot inventory
  emit happens inside `pages.Init`, which runs inside `Mobile.start()`,
  itself called after the new setter — so device model is present on the
  very first event). Real verification is `.github/workflows/
  android-ci.yml`'s `./gradlew assembleDebug` + `guard-gobind-skip.sh`,
  which this repo's CLAUDE.md already names as the authoritative gate for
  `mobile/**`/`android/**` changes.
- No UI surface was touched (no HTML/template edits — the visible
  `#settings-diagnostics` card's markup and behavior are unchanged, only
  which Go code computes its backing data), so no screenshot/visual check
  or `web/help/` update is owed for this card.

## Safe to merge

Yes. `merge_method: "merge"` (never squash/rebase — ut-docs#250).

## Explicitly deferred

- The pre-existing TOCTOU the review noted in `Revoke` (a concurrent local
  `Stop` between `Revoke`'s `Current()` check and its own call into `Stop`
  can report "discarded 0 pending batches" while the target session's
  directory is never actually drained) predates this diff and isn't
  touched or worsened by it — not fixed here, flagged for its own card if
  it's judged worth one.
