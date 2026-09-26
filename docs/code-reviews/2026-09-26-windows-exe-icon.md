# Review — Windows exes carry the app icon + VERSIONINFO (ut-docs#2786)

Date: 2026-09-26 · Lane: cloud-24 · Author model: Opus 5.5 · Reviewer: Fable (independent subagent, isolated worktree)

## What shipped
- `go-winres` v0.3.3 is pinned as a `tool` in `go.mod`, because the goreleaser job can mint the signing token and must run no unpinned tool.
- `packaging/windows/winres/{server,desktop}.json` and `packaging/windows/winres.sh` generate a gitignored `rsrc_windows_amd64.syso` with icon group **32512** (`IDI_APPLICATION`) and VERSIONINFO. `.goreleaser.yaml` runs the script as a `pre` hook on the `windows` and `desktop-windows` builds.
- Why ID 32512: the vendored webview loads the window-class icon with `LoadImage(GetModuleHandle(NULL), IDI_APPLICATION, …)` (`webview.h:2994`). So this one resource covers the title bar and taskbar as well as Explorer, the shortcuts and Apps & features (icon index 0).
- `scripts/ci/checkpeicon` (Go, `debug/pe`) fails closed unless the exe has RT_GROUP_ICON 32512, RT_ICON and RT_VERSION. release.yml runs it on both exes from the portable zip. ci.yml runs it on a cross-compiled `unitill-pos.exe`, which exercises `winres.sh` too.
- NSIS: `MUI_ICON`/`MUI_UNICON` use `ut-logo.ico`.

## Findings (Fable)
Verdict: **safe to merge**, no blocker or major findings.
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The `winres.sh` comment claimed the full snapshot string is embedded, but go-winres writes the numeric version to both fields and strings | Fixed: the comment now says so. Tagged releases are unaffected. |
| 2 | minor | cwd-relative paths in `winres.sh` (correct for both callers) | Hardened: the script now `cd`s to the repo root. |
| 3 | minor | The ci.yml step leaves the .syso behind if `go build` fails | Fixed: `trap … EXIT` |
| 4 | nit | Hooks inherit the desktop build's CGO/mingw env; this works only because go-winres is cgo-free | Added a comment in `.goreleaser.yaml` |
| 5 | nit | The release step had no `test -f "$zip"` | Added |
| 6–7 | nit | The PE parser's uint32 offset arithmetic; the presence-only check | Accepted. Fuzzed with 331 malformed inputs: 0 panics. |
| 8 | process | Review record missing | This file |

## Verified beyond unit tests
- TDD: the reviewer stubbed `check()` to `return nil`; both rejection tests failed, then passed again after restoring it.
- A real goreleaser v2.18.2 `build --snapshot --id windows` ran the hook (`winres.sh server "0.0.0-SNAPSHOT-…"`), and the resulting exe passes `checkpeicon`. The tree stays clean because the .syso is gitignored.
- `go tool go-winres extract` on the built exe shows the six-size logo under #32512, plus FileDescription, CompanyName, ProductName and version.
- makensis 3.09 on Linux builds Setup.exe with the logo as its icon (6 images).
- Gate: `gofmt`, `go build ./...`, full `go test` (plugins with a 20m timeout), `golangci-lint` (0 issues), `shellcheck` and every `build`-job guard. The one exception is `guard-deadcode-baseline{,_test}`, which fails identically on `main` in this sandbox (no GTK headers, so `cmd/unitill-desktop` is skipped); CI's desktop-shell job has them.

## Not verified — deferred
- The Windows VM check (desktop shortcut, Start menu, taskbar, window title bar, Apps & features) needs a human after the next release. It is tracked on ut-docs#2786.
- The desktop exe (CGO + mingw) was not cross-compiled here because this sandbox has no mingw. Its resource config is covered by `TestCheckAcceptsReleaseConfigs/desktop`, and release.yml checks the real exe.
