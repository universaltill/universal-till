# Review: Linux desktop shell re-exec on upgrade + WebKit crash recovery (ut-docs#2991)

Branch `fix/2991-shell-webkit-recovery`. Author: Opus 5.5 (Dev subagent). Reviewer: Fable, independent, fresh context in its own worktree.

## What shipped
- **AC1 — re-exec after an upgrade** (`exe_watch.go`, `exe_reexec_linux.go`): every 30s the shell stats its own executable. When the path names a different file than the running image (the baseline comes from `/proc/self/exe`) and that file looks the same on two checks in a row, the shell `exec`s it. In spawn mode it first stops its own `unitill-pos` (SIGTERM, then SIGKILL after 10s, then reap).
- **AC2 — WebKit recovery** (`webkit_recovery.go`, `webkit_recovery_cgo_linux.go`, `webkit_recovery_linux.go`):
  - `web-process-terminated` reloads the page. A main-frame `load-failed` on the till's own origin reloads too, and `TRUE` suppresses WebKit's error page.
  - Navigation cancellations, policy interruptions (downloads) and plugin-handled loads are ignored.
  - Retries back off at 0, 1, 2, 4, 8, 16s, then every 30s with no limit. At most one reload is pending at a time.
  - Every GTK call goes through `w.Dispatch`. The scheduler closes before `w.Destroy()`.
- **Split out:** AC3 (our own localized recovery page with Reload / Return to desktop) and the device half of AC4 moved to ut-docs#3007.
- **Docs:** `cmd/unitill-desktop/README.md`, plus one sentence in `web/help/{en,de,ar,fa,tr}/updates.md`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Resetting the backoff on `LOAD_COMMITTED` beat the backoff when every web process crashes right after committing: 0s reloads forever (the post-upgrade library case). | **Fixed.** A commit resets the backoff only after the page has stayed up for `recoveryStableAfter` (30s). New test `TestReloadScheduler_CommitThenCrashLoopStaysBackedOff`; it fails against the old reset-on-commit code and passes now. |
| 2 | major | Spawn mode: if exec failed after the child server was stopped, the till was left with no server. | **Fixed.** `access(X_OK)` is checked before anything is torn down. If the exec still fails after the child was stopped, the shell restarts its running image via `/proc/self/exe` and spawns a fresh server. |
| 3 | minor | A child still alive 5s after SIGKILL still led to the exec, which could leave a second server running. | **Logged** as an error. Accepted as rare (D-state). |
| 4 | minor | Re-exec ignores sale/kiosk state. The basket is server-side, so no data is lost, and the new window re-applies the window mode within ~1–3s. | **Accepted.** An upgrade already restarts the server. Noted here. |
| 5 | minor | README said ~30–60s; the real delay is 30–90s. | **Fixed.** |
| 6 | nit | A failing URI was logged in full, and a query could carry a token. | **Fixed.** Only the path is logged. |
| 7 | nit | The zombie/fd-inheritance claims are not verified on a device. | Accepted. The device check is in #3007. |
| 8 | nit | The untagged `exe_watch.go` compiles on darwin/windows. | Matches the `shell_poll.go` convention. |

The reviewer also confirmed:
- The cgo signal prototypes match WebKitGTK's.
- `//export` and preamble rules are followed, and C strings are copied and freed.
- The defer ordering prevents a Dispatch onto a destroyed view.
- The 3 excluded error codes are exactly the ones WebKit's own default handler skips.

## Verified
- `gofmt -l .` empty.
- `go build ./...`.
- `go test ./...` (full repo).
- `go test -race ./cmd/unitill-desktop/...`.
- `CGO_ENABLED=1 go build -tags desktop` and `go vet -tags desktop`.
- `guard-deadcode-baseline.sh`.
- `golangci-lint` (root config and the desktop config): 0 issues.
- Help, i18n, data-access, core-neutral, kiosk-engine and compliance/competitor guards.
- TDD re-verified by the reviewer: mutating change detection, the error-code exclusions and the backoff each fails the matching tests; restoring them makes the tests pass.
- Driven run under `xvfb-run` on WebKitGTK 2.52.6 (Dev):
  - `pkill WebKitWebProcess` → the till reloaded at 0s.
  - With the server down → retries at 1/2/4/8/16s, and the till recovered once the server was back.
  - Binary replaced by rename → the shell re-exec'd with the same PID in both attach and spawn mode, with no orphan server.
- Not driven: the exec-failure fallback (MAJOR 2 fix) and a real Pi. The Pi check is tracked in #3007.

## Verdict
Safe to merge.

## Merge with main (2026-09-27, lane:local)

After #1414 (#2761) and #1439 (#2760) merged, two files conflicted:
`cmd/unitill-desktop/README.md` (two new sections) and
`webview_fallback.go` (two new package-level seams each side). Resolved as a
union — no line of either side removed (checked: `git diff` against both
parents deletes nothing). `go build ./...`, `go test ./cmd/unitill-desktop/`,
and `go vet` for linux/windows/darwin pass; CI re-runs on the merge commit.
