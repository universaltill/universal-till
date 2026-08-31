# 2026-08-31 — desktop shell: `lastAppliedMode` bypass paths (ut-docs#1331)

## What shipped

`controlServer.lastAppliedMode` (`cmd/unitill-desktop/control.go`, backs
`GET /diagnostics`' `current_window_mode`) was previously set only inside
`handleApplyMode` — the `POST /apply-mode` HTTP path, reached on Linux
only via the spawn-mode fallback inside `ShellPollWindowController`. Two
real paths bypassed it entirely, so `current_window_mode` read `""` on
exactly the topology ut-docs#1228's incident happened on:

1. The initial window mode, applied by `showWindow` at launch
   (`cmd/unitill-desktop/webview_fallback.go`).
2. A live mode change on the attach-mode-with-poll path, via
   `watchShellMode`'s callback (same file).

This was a known, deliberately-deferred gap from the review of
ut-docs#1329 (should-fix 3) — flagged there as low-risk to land but
wanting a session that could actually exercise the desktop-tagged code to
confirm.

Fix: a small mutex-protected `SetAppliedMode(mode string)` method on
`controlServer`, mirroring the existing `SetOps` pattern, called from
both bypass sites (each guarded by the file's existing `ctl != nil`
convention).

Files touched: `cmd/unitill-desktop/control.go`,
`cmd/unitill-desktop/control_test.go`, `cmd/unitill-desktop/webview_fallback.go`.

## TDD

`TestControlServer_SetAppliedMode` written first — confirmed it failed to
compile (`cs.SetAppliedMode undefined`) before `SetAppliedMode` existed,
then went green once added.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → Sonnet review, per
model-routing rules), isolated worktree, re-ran the full gate itself and
independently re-verified the TDD claim (temporarily made
`SetAppliedMode` a no-op, confirmed the test fails with
`current_window_mode = "" ... want kiosk`, restored, confirmed green
again — diff against the reviewed commit was empty after restore).

**Verdict: SAFE TO MERGE AS-IS.**

Reviewer specifically chased down the most plausible correctness
question — whether claiming `SetAppliedMode(prefs.WindowMode)`
immediately after a fire-and-forget `applyWindowMode` call overstates
success, especially on Windows where `applyWindowMode` is still #610's
no-op stub. Traced the production wiring
(`internal/pages/common/shell_poll_window_controller.go`) and confirmed
this is not a new gap: `handleApplyMode`'s existing `/apply-mode` path
already treats the identical fire-and-forget `ApplyMode` closure as
authoritative the instant it returns, including on Windows — this diff
extends an already-shipped, already-accepted convention to a previously-
unreported case rather than introducing a new one.

Locking reviewed: `SetAppliedMode` takes the same `cs.mu` full lock
`handleApplyMode` already writes under; no deadlock risk, no missed
unlock path (single non-panicking assignment); `-race` clean on the
package, including the existing concurrent-access test that exercises
the same lock.

One **non-blocking, pre-existing** gap noted (not introduced or worsened
by this diff, and out of this ticket's explicitly-scoped "two paths"):
`POST /exit-to-os` → `handleExitToOS` → `ops.ExitToOS()` →
`applyWindowMode(w, "normal")` never calls `SetAppliedMode("normal")`, so
an exit-to-os triggered via the disconnected/fallback HTTP channel
specifically (not the attached-poll path, which *is* covered — it goes
through `watchShellMode` like any other mode change) leaves
`current_window_mode` stale. Filed as a follow-up Backlog card
(ut-docs#1382) rather than folded into this PR.

## Verified beyond automated tests

- `gofmt -l cmd/unitill-desktop/` — clean.
- `go vet ./cmd/unitill-desktop/...` — clean.
- `go build ./...` (repo-wide, no build tags) — clean.
- `go test ./cmd/unitill-desktop/... -race -v` — all pass, including the
  new test.
- `go test ./...` (full repo) — 42 packages, 0 failures.
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — pass
  (both N/A in substance — no SQL, no user-facing strings — but run for
  completeness per the gate).
- Type/signature correctness of the two `webview_fallback.go` call sites
  checked by eye (both the local session and the reviewer independently):
  matches `prefs.WindowMode string`, `applyWindowMode(w, mode string)`,
  and `watchShellMode`'s `func(mode string, done func())` callback shape
  exactly.
- **Not driven live / no screenshot**: this change has no user-visible
  surface — it's an internal diagnostics field on a loopback control
  channel, not anything a shop owner sees. The `-tags desktop` build
  (the real GTK/WebKit implementation) could not be compiled in this
  container (missing system GTK/webkit2gtk dev libs — a known,
  pre-existing environment limitation documented in
  `cmd/unitill-desktop/stub.go`'s own comment; CI's dedicated
  `desktop-shell` job has those libs). The plain-Go logic this diff
  actually adds (`control.go`) is fully exercised by `go test ./...`
  without the tag; only the two one-line call sites in the tagged
  `webview_fallback.go` could not be compile-checked here, hence the
  manual type-check above.

## Deferred / explicitly out of scope

- The `exit-to-os` fallback-path gap above — new card, ut-docs#1382.
- Darwin's `showWindow` (`webkit_darwin.go`) was checked and confirmed to
  not need this fix: it's a deliberate no-op stub (per its own doc
  comment, ut-docs#609/#882 scope) that never calls `applyWindowMode` or
  `watchShellMode` at all.
