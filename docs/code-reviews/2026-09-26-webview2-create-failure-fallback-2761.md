# Review — Windows desktop: WebView2 start failure falls back to the browser instead of crashing (ut-docs#2761)

Date: 2026-09-26 · Branch: `fix/2761-webview2-create-failure-fallback` ·
Author: Opus 5.5 (pipeline, `lane:cloud-24`) · Reviewer: Fable (independent subagent, one round)

## What shipped

On a Windows 11 ARM64 VM (x64 build under emulation) the app "opened and
closed": `0xc0000005` in `webview_navigate` from `showWindow`. WebView2 had
failed to start (stale `msedgewebview2.exe` processes left over from a
runtime self-update; a reboot fixed it), and we crashed instead of falling
back. Root cause is two bugs in the vendored `internal/thirdparty/webview_go`:

1. `webview.h`'s `win32_edge_engine` ignored `embed()`'s result, so a failed
   WebView2 start still produced a window with `m_webview == nullptr`, and
   `webview_create` returned it as a success.
2. The Go binding's `NewWindow` wrapped even a C `nullptr` in a non-nil
   `*webview`, so `showWindow`'s `w == nil` browser fallback was dead code
   on every platform (Linux with no display included).

Fix (every C++/Go change in the fork is marked `universal-till patch`):

- `NewWindow` returns `nil` on failure; new `LastInitError()` (C:
  `webview_last_init_error()`) exposes the WebView2 `HRESULT`
  (`ERROR_FILE_NOT_FOUND` when no runtime is installed; reset per attempt).
- Windows engine: failed `embed()` → `discard_window()` (wndproc swapped
  first so no stray `WM_QUIT`), so `webview_create` returns `nullptr`.
  `ERROR_INVALID_STATE`/`E_ABORT` now give up once (`give_up()`) instead of
  returning without a callback, which left `embed()` pumping forever behind
  an empty window. Null guards on navigate/init/eval/set_html.
- `embed()` reads `WEBVIEW2_USER_DATA_FOLDER` itself; completion flag and
  user data folder are members; the destructor `detach()`es the COM handler.
- `cmd/unitill-desktop`: `browserFallback` (untagged, CI-tested) opens the
  default browser, wires no-op window ops, owns `ctl.Close()`, and logs the
  reason (HRESULT + runtime version) to `desktop.log`. `webview2_windows.go`
  pins the WebView2 user data folder to `<data dir>\webview2` (`UT_DATA_DIR`
  or `%LOCALAPPDATA%\UniversalTill`; an operator-set variable wins),
  `MkdirAll` first, and logs the Evergreen runtime version (registry `pv`)
  at every start. `shellDataDir` is factored out of `shellLogPath` and shared.
- Manual: `web/help/{en,de,ar,fa,tr}/recovery.md` gain "On Windows: the till
  opens in your web browser instead of its own window"; help-drift baseline
  heading counts bumped for ar/fa/tr (pre-existing bold-lead-in gap
  unchanged). `cmd/unitill-desktop/README.md` documents the behaviour.

## Findings (Fable) and triage

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `webview2DataDir` only reachable from the `windows`-tagged file → `guard-deadcode-baseline.sh` red | Fixed without growing the baseline: shared `shellDataDir` used by `shellLogPath` too |
| 2 | major | `WEBVIEW2_USER_DATA_FOLDER` is applied by Microsoft's WebView2Loader.dll, which we don't ship; the built-in loader passes `userDataFolder` through, so the env var was likely a no-op | Fixed: `embed()` reads the variable itself |
| 3 | major | `give_up()` on `E_ABORT` could now run embed()'s `[&]` lambda (stack `flag`) after embed() returned via `WM_QUIT`, e.g. from `deplete_run_loop_event_queue` in the destructor | Fixed: member `m_embed_pending`/`m_user_data_folder`, `[this]` captures, `detach()` in destructor before `Release()` |
| 4 | minor | `last_init_error` stale across attempts; "not installed" logged as unknown | Fixed: reset at construction; `ERROR_FILE_NOT_FOUND` when no runtime |
| 5 | minor | `TestNew_NoDisplayReturnsNil` fails on a machine with a display once GTK is initialised | Fixed: skips when a display is present at process start |
| 6 | minor | Logged version is the Evergreen registry `pv`, not necessarily the runtime the loader picked | Accepted: matches the reported scenario (Evergreen); noted |
| 7 | nit | `paths.Default()` `./data` fallback when `LOCALAPPDATA` unset | Accepted: same precedent as `shellLogPath` |

Reviewer verified OK: interface-nil semantics, registry lookup (GUID,
`WOW64_32KEY`, HKCU, `0.0.0.0`), `discard_window` teardown order vs the
destructor, `give_up` cannot double-fire or fire after success, and the
de/ar/fa/tr help translations.

## Verification

- TDD: `TestNew_NoDisplayReturnsNil` fails against the upstream `NewWindow`
  ("non-nil, want nil") and passes with the fix — confirmed by the author
  and independently by the reviewer in a separate worktree.
- `TestShowWindow_CreationFailureTakesBrowserFallback` (desktop&&linux):
  injected creation failure → fallback, control listener closed, no crash.
  `TestBrowserFallback_*` (untagged, runs in CI): browser opened, 204 (not
  503) on exit-to-os during fallback, reason with HRESULT in the log.
- Gate: `gofmt -l` clean; `go build ./...`; `go test ./...`; `go test
  -tags desktop -race ./cmd/unitill-desktop/` (GTK/WebKit headers
  installed); `golangci-lint` 0 issues (default and desktop config);
  every `ci.yml` build-job guard passes except the three environment-only
  steps (no `shellcheck` binary, apt PPA 403) — no shell scripts changed.
- Windows: `CGO_ENABLED=1 GOOS=windows` mingw-w64 cross-build and `go vet
  -tags desktop` of `cmd/unitill-desktop` succeed with the patched C++.
- **Not verified on real Windows.** A driven run under wine was attempted
  (fake till on :8080, attach mode): both the `main` build and this build
  hang inside wine's COM/RPC setup (`RPC_S_SERVER_UNAVAILABLE`) before any
  WebView2 code loads, so that run is inconclusive either way. A
  real-Windows check (the ARM64 VM: runtime present → normal window and
  `<data dir>\webview2\EBWebView` created; runtime removed or stale
  processes → browser fallback + log line) is filed as a follow-up card.

## Verdict

Safe to merge: the crash path is closed at both the C++ and Go layers, the
fallback is unit- and regression-tested, and the review's blocker and both
majors are fixed. One-time effect: WebView2 cookies move from
`%APPDATA%\unitill-desktop.exe` to `<data dir>\webview2`, so an existing
Windows till asks for login/language once (no real shops yet).
