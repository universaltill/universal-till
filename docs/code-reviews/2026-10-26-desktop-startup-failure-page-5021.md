# Review: desktop shell shows "could not start" instead of a white window (ut-docs#5021)

## What shipped

- `cmd/unitill-desktop` reaps the spawned `unitill-pos` on **every** OS. On
  macOS the reap goroutine was skipped (the Cocoa close handler SIGTERMs the
  server by PID), so a server the kernel SIGKILLed ~170 ms after exec (field
  report: invalid code signature) looked like a 10 s timeout and the window
  opened on a dead URL — a blank page. The PID is now handed to Cocoa at
  spawn time (`UTSetChildPid`) and atomically cleared right after reaping
  (`UTClearChildPid`), so a reaped PID is never signalled; the remaining
  microsecond race is documented at `gChildPid`.
- On `childExited` (all OSes) the shell shows a self-contained page
  (`startup_failure.go`, untagged, `html/template`): what happened, "open it
  again", "reinstall — keeps your data", the logs folder holding
  `desktop.log`/`till.log`, and the exit reason. `childTimedOut` keeps the
  old behaviour (open the till anyway).
- Translations from the embedded `web/locales` FS with per-key English
  fallback (same policy as `cmd/unitill-uninstall/locale.go`); language from
  `LC_ALL`/`LC_MESSAGES`/`LANG`, else macOS `NSLocale preferredLanguages`,
  else English; a language without a locale file falls back to `en` for
  `lang`/`dir` too.
- New keys `desktop.startup_failed.*` in en/ar/fa/tr + `en.meta.json`
  metadata (`desktop` namespace mapped to the `tills` area); de/es/pt
  pack PRs follow the core merge.
- Manual: `web/help/{en,de,ar,fa,tr}/recovery.md` gains "If the app opens a
  'could not start' window"; help-drift baseline updated for the
  already-recorded ar/fa/tr entries.
- New path-scoped workflow `desktop-darwin.yml` builds and vets the darwin
  shell on `macos-14` — before this, `webkit_darwin.go` was compiled only by
  release.yml (never a required check; listed in `CLAUDE.md`).

Out of scope, filed: ut-docs#5049 (the `no such table` flood: no in-app path
swaps the DB; an externally deleted data folder makes new pool connections
create an empty DB — open read-write, watchdog, uninstaller waits).

## Review

Built by Sonnet; reviewed independently by Opus 5.5 (fresh context, own
worktree). Verdict: safe to merge, no blocker/major.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `lang`/`dir` came from the requested language, not the one shown — `LANG=he_IL` gave English text laid out RTL | **Fixed**: `availableShellLang` + `TestAvailableShellLang` (fails without the fix, verified) |
| 2 | minor | Windows with no working WebView2: the page is only logged, nothing shown | Accepted, ut-docs#5051 (temp-file + browser fallback) |
| 3 | minor | Windows has no platform language hint (no `LANG`) → English page | Accepted, ut-docs#5051 |
| 4 | minor | Manual called it a "small window"; it is 1280×860 | **Fixed** in all five languages |
| n | nit | tr body dropped "henüz" | **Fixed** |
| n | nit | `_ = childPid` unnecessary | **Fixed** |
| n | nit | `desktop-darwin.yml` ignores `go.mod` changes | **Fixed** (`go.mod`/`go.sum` added); `internal/*` imports left out on purpose to keep macOS minutes bounded |
| n | nit | en `logs` string doesn't say "to support" | Accepted: wording matches ar/tr; the page has no support channel to name |

TDD re-verification (reviewer, in its worktree): seven mutations
(`textDir` always ltr, `text/template`, `shellLang` ignoring `LC_ALL`, key
removed from tr.json, no lang-code validation, no empty-value fallback,
C/POSIX not ignored) each failed the matching test; green on restore.

## Verified beyond unit tests

- Rendered the page (en, fa, tr) with Playwright at 1024×600 and 360 px and
  looked at the screenshots: no horizontal scroll, RTL correct for fa, the
  path wraps inside its box.
- Darwin cannot be compiled here (no SDK): the reviewer type-checked
  `desktop.go` for `GOOS=darwin` with the cgo calls stubbed and compiled the
  C/Objective-C fragments with clang `-Wall -Wextra`; the new
  `desktop-darwin.yml` compiles the real thing on this PR.
- Linux desktop-tag build: GTK/WebKit headers unavailable in this container;
  covered by ci.yml's desktop job.
- **Not verified on a real Mac**: the page appearing after a real early
  child death. The owner's Mac (reinstall from the DMG first, per the card)
  is the device check.

## Safe to merge

Yes, once CI (including `desktop-darwin`) is green.
