# Review — desktop/Windows log file + Settings → Diagnostic mode log card (ut-docs#2720)

Date: 2026-09-25 · Branch: `feat/2720-windows-log-file` ·
Author: Opus 5.5 (pipeline, `lane:local`) · Reviewer: Fable 5.1 (independent subagent)

## What shipped

A Windows till upgraded to v0.22.2 never checked in with the cloud and left
no trace: the till logged to stdout only, and a Windows GUI launch discards
it. Now:

- **`internal/logging`** gains a size-rotating file sink (`rotate.go`,
  5 × 5 MB, oldest dropped, nothing buffered in memory; Windows
  sharing-violation fallback copies-then-truncates the live file), a
  free-text redactor (`redact.go`: Bearer/Basic, secret-named `k=v`,
  URL userinfo, JWTs, unlabelled high-entropy 32+ char runs; UUIDs, paths,
  hosts and versions survive), `AttachFile`/`DetachFile` that tee both this
  package's logger and the stdlib `log` package into the redacted file, and
  `Stderr()` for the desktop shell's plain `fmt.Fprint` messages
  (timestamped + redacted). `ResolveFile` implements `UT_LOG_FILE`:
  empty = on at `<data dir>/logs/till.log` for desktop/server OSes, off on
  Android/iOS; `0` off; `1` on; a path relocates (made absolute, never
  cwd-relative).
- **`internal/app`**: attaches the file right after config resolves (before
  the data-dir lock, so a second instance's lock failure is logged too) and
  emits one `startup:` line after `enroll.Init` — version, OS, data dir,
  the pos.env actually loaded (absolute), cloud **host only**, enrolled
  yes/no, store id **suffix**, role, log file.
- **`cmd/unitill-desktop`**: writes its own `desktop.log` beside `till.log`
  (same `UT_LOG_FILE`/`UT_DATA_DIR` resolution) and routes every former
  `os.Stderr` message through `logging.Stderr()`.
- **Settings → Diagnostic mode** (manager-only card): a **Log files** section
  showing the folder with *Copy folder path*, and a collapsible diagnostics
  summary (the startup line recomputed live) with *Copy diagnostics*; a
  delegated `[data-copy-target]` handler in `app.js` with an `execCommand`
  fallback for non-secure contexts (a till opened over LAN by IP).
- 7 new `settings.diagnostics.*` keys in en/ar/fa/tr; help `recovery`
  (new "Where the till's log is" section, all 5 locales) and `display`
  (step 19); `help-drift-baseline.json` entries for ar/fa `recovery` shifted
  by the same delta; README `UT_LOG_FILE`.

## Findings (Fable 5.1)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `settings.diagnostics.log_off` said "turned off with UT_LOG_FILE=0" — false on every Android/iOS till (off by design) and when the log folder cannot be created (attach failure is best-effort). | **Fixed** (reviewer) — reworded in en/ar/fa/tr; `guard-i18n` green. |
| 2 | nit | `paths.Logs()` is exported and documented but has no production caller (`ResolveFile` builds the same path from `cfg.DataDir`, which *is* `paths.DataDir()`); only its test uses it. | accepted — harmless, keeps the "<data root>/logs" contract in one named place; drop or wire it if it stays unused. |
| 3 | nit | `authSchemeRe` redacts the word after any "basic"/"bearer" in prose (`basic setup done` → `basic [REDACTED]`). No log message in the tree contains either word today; the redactor deliberately errs toward over-redaction. | accepted |
| 4 | nit | `log_intro` hard-codes "till.log.1 to till.log.4" and "25 MB"; a change to `DefaultMaxFiles`/`DefaultMaxFileBytes` must revisit the string and the help. | accepted — noted in the constants' comment would be enough. |
| 5 | obs. | `--install-autostart` (postinstall, `runuser -u <desktop user>`) now also creates `<that user's data dir>/logs/desktop.log` with one startup line. Correct user, correct place; just new behaviour to know about. | accepted |
| 6 | obs. | The `startup:` log line's `enrolled=` reflects state at boot; `enroll.Init` registers in the background, so a fresh till logs `enrolled=no` even when registration succeeds seconds later. The Settings summary is recomputed live, so "Copy diagnostics" is always current. | accepted |
| 7 | follow-up | The 7 new `en.json` keys need the usual `ut-plugin-language-{de,es}` follow-up PRs (`lang-pack-drift.yml` will warn on this PR). `web/help/de/recovery.md` is already updated here. | for the orchestrator |
| 8 | follow-up | `guard-docs-shots` fails as expected: `web/help/*/display.md`, `web/ui/partials/diagnostics_block.html` and `internal/pages/**` changed. The new card section is a real pixel change, so `make docs-shots` (last and alone, per memory) is needed before merge — not run in this review. | for the orchestrator |

No blocker/major. Security review: file 0o600 in a 0o755 dir under the
per-user data root (`%LOCALAPPDATA%\UniversalTill\logs` on Windows —
already user-private); the startup line prints the cloud **host** only
(never userinfo/path/query) and the store id's last 4 chars; the log folder
and summary render only inside the `{{ if .isManager }}` card and
`diagnosticsViewIfManager` returns a zero view for cashiers (tested). A
custom `UT_LOG_FILE` path is made absolute at resolve time. Nothing is
buffered — the disk budget (25 MB) is the only bound, and
`TestRotatingWriterIsBounded` proves it at ~50× the budget.

Tests were checked for false passes: every rotation test asserts on real
files in a `t.TempDir()`; the redaction tests assert both that the secret is
gone *and* that a marker was inserted; `TestRun_WritesLogFileWithStartupLine`
boots the real `app.Run` and reads the file back;
`TestSettingsPage_DiagnosticsCardShowsLogFolder` renders `/settings` through
the real mux for a manager and a cashier.

## Verified beyond automated tests

- Read the whole diff plus every untracked file; traced the desktop shell's
  child env (`UT_DATA_DIR`/`UT_LOG_FILE` are inherited unchanged, so
  `desktop.log` and `till.log` resolve to the same folder) and confirmed
  `unitill-pos.service` pins `UT_DATA_DIR=/opt/unitill/data`, matching the
  help's "installed as a service" path.
- Windows specifics: the live file is closed before rename; rename failure
  (another process holding `till.log`) falls back to copy-then-truncate and
  keeps the newest content (`TestRotatingWriterRenameFailureKeepsNewestContent`);
  `teeWriter` ignores write errors so a nil/invalid `os.Stdout` on a GUI
  launch cannot stop file writes; `%q` in the startup line keeps backslash
  paths from being mistaken for token runs by the redactor.
- Gate: `gofmt -l` clean, `go build ./...`, `go vet`, `golangci-lint` 0
  issues; `go test -race -count=1` green for `internal/logging`,
  `internal/paths`, `cmd/unitill-desktop`, `internal/app` and
  `internal/pages` (the last with the Makefile's long timeout — the bare
  600s default is not enough for that package under race, as documented);
  guards `i18n`, `help-topics`, `help-drift`, `compliance-claims`,
  `competitor-naming`, `readme-local-links`, `data-access`,
  `page-http-error`, `kiosk-engine`, `plugin-menu-read` all pass;
  `docs-shots` fails only for the reason in finding 8.
- Not run here: e2e and `make docs-shots` (out of scope for this review).

**Verdict: safe to merge once `make docs-shots` is regenerated and the
language-pack follow-ups are filed.**
