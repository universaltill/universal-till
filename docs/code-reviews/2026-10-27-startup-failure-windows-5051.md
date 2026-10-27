# Review: Windows startup-failure page without WebView2, in the Windows UI language (ut-docs#5051)

Follow-up from ut-docs#5021 (review findings 2 and 3,
`2026-10-26-desktop-startup-failure-page-5021.md`).

## What shipped

- **No WebView2:** `showStartupFailure` (`webview_fallback.go`) used to log
  and return when the system WebView could not be created, so the operator
  saw nothing. It now hands the page to `startupFailureInBrowser`
  (`webview_browser_fallback.go`, untagged): the static page is written to
  an `os.CreateTemp` file (0600, random name) in `os.TempDir()` — a relative
  dir is refused, so the path is never cwd-relative — opened in the default
  browser by its escaped `file://` URL (`fileURL`), kept for 5 minutes
  (`startupFailureLinger`), then removed; a failed remove (browser holding
  the file on Windows) is logged and left to the OS temp cleanup. The dead
  server URL is still never opened.
- **Language:** `platformPreferredLang` on Windows (`ui_lang_windows.go`,
  `//go:build windows` so CI's `GOOS=windows go vet` checks it) returns the
  first entry of `GetUserPreferredUILanguages(MUI_LANGUAGE_NAME)` — the
  display language, rather than the regional-format locale the card
  proposed (`GetUserDefaultLocaleName`): the page is UI text, and the
  wrapper already exists in `golang.org/x/sys/windows`. LC_ALL/LC_MESSAGES/
  LANG still win; an unsupported language still falls back to English.
  The "" stub moved from `webview_fallback.go` to `platform_lang_other.go`
  (`desktop && !darwin && !windows`).
- Manual: `web/help/{en,de,tr,ar,fa}/recovery.md` — the "could not start"
  section says the page follows the computer's language when the till has
  it, and opens in the browser on Windows without WebView2.
  `cmd/unitill-desktop/README.md` updated.

## Review

Built by Sonnet; independent review by Opus 5.5 in a separate worktree.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | Help said "see the last section below" — the WebView2 section is the next one | should-fix | Fixed |
| 2 | Help claimed "your Windows display language" unconditionally; env vars win and unsupported languages fall back to English | should-fix | Fixed (reworded, all platforms) |
| 3 | de/tr/ar/fa help not updated | should-fix | Fixed (same sentences in all four) |
| 4 | 30 s linger too short behind a browser first-run wizard; reload after 30 s shows file-not-found | should-fix | Fixed: 5 minutes (the shell is exiting anyway; the child is already dead, `ctl` serves no-ops) |
| 5 | `os.Remove` error dropped; raw path with spaces to `rundll32 FileProtocolHandler` unverified | nit | Fixed: remove failure logged; browser gets an escaped `file://` URL (`TestFileURL`) |
| 6 | Snap/Flatpak browsers on Linux can't read the host `/tmp` | nit | Accepted: only when WebKitGTK fails with a display present (rare); help promises the browser path on Windows only |
| 7 | `platformPreferredLang` undefined for a `desktop` build on other OSes (freebsd) | nit | Fixed: `platform_lang_other.go`; `GOOS=freebsd go vet` clean |
| 8 | `"rel"` relative-dir case passed even without the `IsAbs` check (dir didn't exist) | nit | Fixed: test creates `rel/`; re-verified it fails with the check removed |
| 9 | README line too long | nit | Fixed |

Reviewer also checked and found no problem: `html/template` escaping of the
exit reason, `<meta charset>` for file-opened pages, the linger blocking
nothing (child already reaped; no single-instance lock), error joining.

## Verification

- TDD: tests written first (compile failure on the missing helpers), then
  behavioural mutation re-verification by the reviewer: dropping the remove,
  the `IsAbs` check, `TrimSpace`, linger-before-open and last-entry-instead-
  of-first each fail a named assertion; fix 8 re-verified inline.
- `gofmt -l`, `go vet` (linux, `GOOS=windows`, `GOOS=darwin`, `GOOS=freebsd`),
  `go test ./cmd/unitill-desktop/` (also `-tags desktop`), `go build -tags
  desktop` + vet on Linux (GTK/WebKit headers installed), mingw cross-build of
  `unitill-desktop.exe -tags desktop`, desktop golangci-lint (0 issues),
  `golangci-lint run ./...` (0 issues), `go test ./...`, guard-help-drift,
  guard-help-topics, guard-help-breadcrumbs, guard-i18n,
  guard-compliance-claims.
- `guard-deadcode-baseline.sh` could not run here (x/tools v0.48 deadcode
  rejects go1.27 packages in this container) — fails identically on `main`;
  CI runs it.
- **Not verified:** `TestWin32PreferredUILanguage` and the browser opening
  on a real Windows machine without WebView2 — no Windows host here. The
  test runs in CI's `windows-shell` job (`-run "Win32|..."`).

## Verdict

Safe to merge once CI (incl. `desktop-shell` and `windows-shell`) is green.
