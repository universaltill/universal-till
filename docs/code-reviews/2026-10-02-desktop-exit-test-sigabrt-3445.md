# Review: desktop exit-to-OS test — isolate the native teardown SIGABRT (ut-docs#3445)

- **Branch**: `fix/3445-desktop-exit-test-sigabrt`
- **Author**: Sonnet, inline in the orchestrator's session — **not** the
  pipeline's usual Opus Dev subagent. Process deviation, recorded here; it
  does not change what was verified below.
- **Reviewer**: Fable, as an independent subagent with a fresh context, a
  different model from the author.
- **Date**: 2026-10-02

## Scope

One file, test-only, behind `//go:build desktop && linux`:

- `cmd/unitill-desktop/webview_fallback_exit_linux_test.go`

No production code, no CI change. `.github/workflows/ci.yml` builds and vets
with `-tags desktop` but never runs `go test -tags desktop` (confirmed by
grep), so this test only ever runs on a developer's real display or under
Xvfb. The untagged `go test ./...` path never compiles this file at all.

## The problem and the investigation already done

`TestDesktopWindowOps_ExitToOSRecordsAppliedMode` drives a real GTK/WebKit
window through `desktopWindowOps`'s `ExitToOS` closure and polls
`GET /diagnostics` until `current_window_mode == "normal"` (the ut-docs#1382
fix). Under Xvfb it SIGABRTs inside `webview_destroy` on roughly 10-20% of
runs (the issue measured 2/19 and 3/20). It is not the ut-docs#1382 review's
threading bug: `New`/`Run`/`Destroy` already stay on one goroutine and only
`Terminate` is called cross-goroutine.

The author ruled out, with 40-iteration loops each, none moving the rate
outside binomial noise:

- a missing window manager under Xvfb (ran `matchbox-window-manager`);
- `JSC_SIGNAL_FOR_GC=0` (made every run crash immediately and differently —
  JSC treats 0 as a real signal number, so this is disproven, not a fix);
- `WEBKIT_FORCE_SANDBOX=0`;
- a 300ms and a 1000ms settle delay before `Destroy`.

Every captured crash log already contains control.go's diagnostics line
`window_mode="normal"` before the abort, i.e. the behaviour under test had
completed; the crash is strictly in native teardown afterwards. That matches
the issue's non-goal (the real shell under a real display is not known to be
affected) and its acceptance criterion allowing the test to be restructured
to avoid the race if it is unfixable in its current shape.

## What shipped

- The test re-execs itself (`os.Args[0]`, the same pattern as
  `internal/logging`'s `TestFatalfExitsProcess`) with
  `-test.run=^TestDesktopWindowOps_ExitToOSRecordsAppliedMode$ -test.v` and
  `UT_TEST_DESKTOP_EXIT_CHILD=1`. The child runs the unchanged real body
  (`runDesktopExitToOSChild`), so a native abort can no longer take `go
  test` down with it.
- The parent retries, bounded at 5 attempts, **only** when the child's output
  proves both that the fix landed (`window_mode="normal"` present) and that
  the failure is Go's fatal-signal-in-cgo crash dump (`"SIGABRT: abort"` and
  `"signal arrived during cgo execution"` both present). Anything else fails
  immediately: a `--- FAIL:` line (the assertion itself), a crash before the
  proof line, a crash with a different shape, or (after this review) a hang.
- The banner-text check replaced the author's first attempt at checking the
  wait status for a signal. That was wrong, not just fragile: the Go runtime
  catches the fatal signal, prints the dump and exits 2, so the kernel never
  reports WIFSIGNALED. The author found this empirically (2 unexplained
  failures out of 25 with the wait-status check, 0 out of 45 with the banner
  check); I confirmed it in the runtime source (`signal_unix.go`'s fatal
  path prints `signal arrived during cgo execution` then `exit(2)`;
  `"SIGABRT: abort"` is sigtab entry 6).

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The child had no timeout. A test binary run directly has no `-test.timeout`, and `cmd.CombinedOutput()` blocks indefinitely, so a wedged GTK/WebKit loop (instead of a crash) would have stalled until `go test`'s own 10-minute parent timeout panicked the **parent** and orphaned the child plus WebKit's helper processes, which inherit the child's stdout pipe. | **Fixed.** New `runDesktopExitToOSSubprocess` helper uses `exec.CommandContext` with a 60s deadline (`desktopExitSubprocessTimeout`; a healthy run is ~0.15s, the child's own poll gives up at 5s) and `cmd.WaitDelay = 5s` so a lingering helper process holding the pipe cannot block the wait. A timed-out child is a distinct `t.Fatalf`, never retried. |
| 2 | design, accepted | `crashedWithSIGABRT` retries any abort-in-cgo that happens after the proof line, not only one inside `webview_destroy` — a SIGABRT during `Terminate`'s dispatch or `Run`'s unwind would also match. | **Accepted.** After the proof line the child runs only `Terminate` → `Run` returns → `Destroy`, all webview_go C; no till code executes there, so what is retried is exactly "native teardown after the verified behaviour". The shapes that must still fail do: a Go `panic:`/`fatal error:` prints no `SIGABRT: abort` banner, an OOM kill is SIGKILL with no dump, a C abort before the proof line lacks `proved`. All fall to the immediate `t.Fatalf`. |
| 3 | design, accepted | Exhausting all 5 attempts fails the test rather than passing with a loud log. | **Accepted, correct call.** At the measured 10-20% rate, five consecutive aborts has probability 1e-5 to 3e-4; if it happens the teardown has become (near-)deterministically broken, which is a real regression signal and should be red. The message says exactly that. Statistical bad luck at these odds is a reasonable price. |
| 4 | nit, accepted | `-test.v` on the child is load-bearing, not cosmetic: without it a skipping child prints only `PASS`, and the parent would report a pass instead of a skip. | Documented in the new helper's comment. |
| 5 | nit, accepted | `TestFatalfExitsProcess` anchors only the end (`TestFatalfExitsProcess$`); this test anchors both ends. Stricter than the precedent, consistent in spirit. `TestControlServer_ExitToOSRoutesToOps` is the only other test with `ExitToOS` in its name and cannot match. | Accepted. |
| 6 | nit, accepted | The exit code (2 for a runtime fatal signal) is not checked alongside the banner. | Accepted; the two banner lines are already specific to this crash shape, and an exit-code check would add nothing the banner does not. |
| 7 | process | Author was Sonnet inline rather than the Opus Dev subagent. | Recorded; the work was reviewed to the same standard. |

Also checked and found fine:
- **False-match on the proof line.** `controlServer.lastAppliedMode` defaults to
  `""`, so the diagnostics line reads `window_mode=""` until something calls
  `SetAppliedMode`. Within this test's reach only `desktopWindowOps.ExitToOS`
  (`webview_fallback.go:236`) sets `"normal"`; the other two call sites
  (`showWindow`'s initial apply, `watchShellMode`) are not exercised. A
  direct run shows the two lines in sequence, `window_mode=""` then
  `window_mode="normal"`.
- **Output capture.** `logging.L()` writes to `os.Stdout`, so the proof line
  is in `CombinedOutput`; the child's `-test.v` output and the runtime's
  stderr crash dump are too.
- **Retry branches, walked through.** `err == nil` → pass, or skip if the
  child skipped. `--- FAIL:` → fail now (checked before the retry condition,
  so a child that fails and then also aborts still fails). Not proved, or
  proved without the banner → fail now. Timed out → fail now. Proved and the
  banner → log and retry. There is no branch that retries anything other
  than the known shape.
- `errors` is still used (`errors.New` in `driveExitToOS`, now also
  `errors.Is`). No unused imports; `gofmt`, `go vet` and `golangci-lint`
  clean with and without the tag.
- The child inherits the environment (so `DISPLAY` reaches it) but none of
  the parent's test flags; `-count`, `-race` etc. only govern the parent.
  Fine for a single-shot child.

## Verification (run by the reviewer, not re-read)

Environment: `pkg-config --exists gtk+-3.0 webkit2gtk-4.1` ok; Xvfb `:99`
with `matchbox-window-manager` already running, reused.

- `gofmt -l cmd/unitill-desktop/webview_fallback_exit_linux_test.go` — empty,
  before and after finding 1.
- `CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop/... && go vet
  -tags desktop ./cmd/unitill-desktop/...` — ok.
- `go build ./... && go vet ./...` (untagged, the normal CI path) — ok.
- `golangci-lint run ./cmd/unitill-desktop/...` — 0 issues; also 0 issues
  with `--build-tags desktop`.
- Regression loop, 20 × `DISPLAY=:99 CGO_ENABLED=1 go test -tags desktop -run
  TestDesktopWindowOps_ExitToOSRecordsAppliedMode -count=1
  ./cmd/unitill-desktop/...`: run twice, once on the WIP snapshot and once
  after finding 1. Both 20/20 `ok`, 0 `FAIL`. In the second loop iteration 18
  took 0.422s against a ~0.23s norm, which is the retry path firing once and
  recovering, as designed.
- Skip path: with `DISPLAY=` empty the parent reports `--- SKIP` and `ok`,
  not a failure.
- Direct-body path: `UT_TEST_DESKTOP_EXIT_CHILD=1` in the parent's own env
  runs the real body in-process on `:99` — `--- PASS` in 0.14s, with the
  `window_mode=""` → `window_mode="normal"` progression in the log.
- **Not exercised:** the timeout branch itself (would need a deliberately
  wedged child). It is a straight `exec.CommandContext` + `ctx.Err()` check
  and reads correctly; noted honestly rather than claimed.

## Verdict

**Safe to merge.** This is a test-only change in a file CI never executes,
and it does not weaken what the test verifies: the assertion still fails
immediately on any genuine failure, and the only outcome that is retried is
a crash shape the author characterised empirically and I confirmed against
the runtime source, occurring strictly after the verified behaviour has
been logged. The one real gap (an unbounded child) is fixed in this review.
The empirical dead ends are recorded in the test's doc comment so the next
person does not repeat them.
