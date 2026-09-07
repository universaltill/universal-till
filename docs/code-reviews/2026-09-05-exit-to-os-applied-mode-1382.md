# Code review: exit-to-os fallback path never recorded applied mode (ut-docs#1382)

## What shipped

`cmd/unitill-desktop/webview_fallback.go`: the `ExitToOS` closure `showWindow`
wires into the desktop shell's local control server never called
`ctl.SetAppliedMode("normal")` after dispatching `applyWindowMode(w, "normal")`
on the real GTK window. So `POST /exit-to-os` on the **disconnected/fallback**
HTTP channel (used when this process isn't attached via the polled
`ShellPollWindowController` channel) left `GET /diagnostics`'s
`current_window_mode` stale at whatever it reported before the exit — e.g.
still `"kiosk"` after the window had actually returned to normal.

The already-correct sibling paths, confirmed by reading the real code rather
than trusting the issue's own description:
- `handleApplyMode` (`control.go`) records `cs.lastAppliedMode = mode` itself
  after `ops.ApplyMode(mode)` returns nil.
- `showWindow`'s initial-apply call and its `watchShellMode` live-poll
  callback both already call `ctl.SetAppliedMode` (ut-docs#1331).

Only the disconnected exit-to-os path was missing the call. Same class of gap
as ut-docs#1331, not worsened or introduced by it — filed separately per that
card's own note.

## Fix

- Extracted the `windowOps` construction out of `showWindow` into a new
  `desktopWindowOps(w webview.WebView, ctl *controlServer) *windowOps` — a
  pure refactor (byte-identical `ApplyMode` closure body, confirmed via diff)
  that makes the ops construction callable independent of `showWindow`'s own
  startup machinery (`waitForSafeStartup`, `fetchShellPrefs`,
  `reconcileAutostart`, …).
- Added `ctl.SetAppliedMode("normal")` inside the `ExitToOS` closure, right
  after `applyWindowMode(w, "normal")` — same pattern as the already-shipped
  `watchShellMode` callback and the initial-apply call site in the same file.

No SQL, no money, no i18n strings, no kiosk-engine routes, no `web/` or
`internal/pages` files touched — confirmed by `git diff --name-only`, not
assumed. No shop-owner-visible behaviour change, so the UX-guidelines
checklist and the user-manual-topic rule don't apply here.

## Independent review (fresh-context Sonnet subagent, isolated worktree)

First round found a real blocker in the new regression test (not in the
production fix): the test called `go w.Run()` (GTK main loop on a spawned,
unlocked goroutine) and `defer w.Destroy()` on the test's own, different
goroutine. `webview_go`'s vendored library expects `New`/`Run`/`Destroy` to
execute on one consistent goroutine — exactly how production's own
`showWindow` already uses it (sequential, no goroutine hop) — and calling
`Destroy()` from a different thread than the one running `Run()`'s loop
aborted inside `webview_destroy` (`SIGABRT`) in 4 of 5 uncached runs. Not a
CI-breaking issue today (`desktop-shell`'s CI job only runs
`go build`/`go vet -tags desktop`, never `go test -tags desktop` — confirmed
in `.github/workflows/ci.yml`), but a landmine for the eventual ut-docs#1581
real test pass and for any developer running it locally.

Fixed by moving the HTTP-driving/polling logic (`driveExitToOS`,
`currentWindowMode`) to a background goroutine that only ever calls
`w.Terminate()` — explicitly documented safe cross-thread — while `New()`,
`Run()`, and `Destroy()` all stay on the test function's own goroutine,
mirroring production's thread discipline exactly. `testing.T.Fatal` cannot be
called from a non-test goroutine, so the background goroutine reports its
outcome over a channel instead of reusing `control_test.go`'s
`t`-taking `authedPost`/`authedGet` helpers.

Second round (self-verified, same rigor as the first): 5 uncached runs of
the fixed test, all pass; golangci-lint clean; build/vet clean in both
build configurations.

The reviewer's other findings (all confirmed correct, no changes needed):
- `ctl` is guaranteed non-nil at every call site inside `desktopWindowOps`'s
  closures (it's a function parameter never reassigned, only called from
  inside an existing `if ctl != nil` guard).
- The new test file's build tag (`desktop && linux`) matches existing
  convention exactly (`autostart_linux_test.go`, `window_mode_gate_linux_test.go`).
- No secrets, no real client/shop names.
- No `paths.Data`/missing-`MkdirAll`/cwd-relative-path issue (nothing in this
  diff writes to disk).

## Verification (this pass, after the test fix)

- `gofmt -l .` — clean.
- `go build ./...` and `go build -tags desktop ./cmd/unitill-desktop` — both clean.
- `go vet ./...` and `go vet -tags desktop ./cmd/unitill-desktop` — both clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite green.
- `xvfb-run -a go test -tags desktop ./cmd/unitill-desktop/...` — green,
  including the new test, run 5 times uncached with zero failures (real
  GTK/WebKit window, via installed `libgtk-3-dev`/`libwebkit2gtk-4.1-dev` +
  Xvfb in this environment).
- `golangci-lint run ./...` — 0 issues.
- All CI-blocking guards under `scripts/ci/` — all pass (this change touches
  none of their subject matter, but ran the full list per `CLAUDE.md`'s
  "Before committing" checklist).
- TDD mutation check, done twice (once per test-threading version): revert
  the added `ctl.SetAppliedMode("normal")` line, re-run — fails every time
  with the intended message (`current_window_mode never became "normal"...`),
  no crash on the corrected-threading version; restore the line, re-run —
  passes every time.

## Safe-to-merge verdict

Yes. Production fix is a minimal, correct, well-precedented one-line addition
plus a behaviour-neutral extraction; the regression test — after the
threading fix the independent review's first round required — passes
reliably and is proven to actually catch the regression it exists for.

## Deferred / out of scope

- `ut-docs#1581` (a `-tags=desktop` golangci-lint pass and whole-program
  `deadcode -tags=desktop` baseline over `cmd/unitill-desktop`) is unrelated,
  pre-existing, tracked separately — not touched here.
- No behaviour change to the `ApplyMode` closure or any other path in this
  file.
