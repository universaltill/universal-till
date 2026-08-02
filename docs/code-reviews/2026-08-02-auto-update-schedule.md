# Code review: auto-update toggle + schedule

**Date:** 2026-08-02
**Scope:** `internal/pages/update_api.go`, `internal/pages/update_api_test.go`,
`internal/pages/init.go`, `internal/pages/settings_page.go`,
`internal/selfupdate/selfupdate.go`, `internal/selfupdate/selfupdate_apply_test.go`,
`internal/app/app.go`, `web/ui/pages/settings.html`,
`web/locales/{en,ar,fa,tr}.json`.
**Trigger:** universaltill/ut-docs#79, "Auto-update for the till app (toggle
+ schedule)".

## What shipped

A settings toggle + time-of-day schedule so the existing
`internal/selfupdate.Apply` (download, verify, swap, re-exec) can run
unattended, instead of only via the existing manual "Update now" button.
Design deliberately mirrors the already-accepted EOD-scheduler pattern
(`internal/pages/eod_api.go`): same settings-key shape
(`update.auto_enabled`/`update.auto_time`/`update.auto_last_attempt`), same
pure decision function (`autoUpdateDue`), same 30s-ticker background
goroutine (`StartAutoUpdateScheduler`, wired in `init.go` next to
`StartEODScheduler`), same manager-gated HH:MM-validated settings handler
(`POST /api/settings/update-schedule`). No ADR needed — no new architectural
precedent; ADR-0003 (offline-first) already governs, and the scheduler is a
background-only goroutine with no checkout-path dependency. New settings-page
UI (checkbox + time input in the existing "Software update" card) and one new
locale key (`settings.update.auto_enable`, all 4 locales; reused
`common.save` for the button).

## New tests

`autoUpdateDue` (pure, table-driven), the `POST /api/settings/update-schedule`
handler (manager gate, HH:MM validation, round-trip persistence), and
`autoUpdateTick` (six cases covering not-due, cached-unavailable,
unsupported, basket-in-progress, applies-once, fresh-recheck-avoids-stale,
failed-apply-no-same-day-retry) using new package-level test seams
(`autoUpdateCurrent`/`autoUpdateCheckNow`/`autoUpdateSupported`/
`autoUpdateApply`) — never hits the real GitHub API or the real
`selfupdate.Apply` (which would try to re-exec the test binary).

## Verification (self + Tester, before independent review)

- `go build ./... && go vet ./...`: clean.
- `go test ./...`: green except the pre-existing
  `TestSaveCleansUpDirectoryOnWriteFailure` (`internal/issuereport`) —
  confirmed unrelated: fails identically on unmodified `main` (root bypasses
  the read-only-directory permission it relies on), package untouched by
  this diff.
- `guard-data-access.sh`, `guard-i18n.sh`: green.
- Drove the real running app (Playwright, headless Chromium, `UT_AUTH=off`):
  loaded `/settings`, checked the toggle, set `03:30`, saved, reloaded,
  confirmed both values persisted through the real HTTP handler and
  DB-backed settings store. Switched to `?lang=fa`, confirmed `dir="rtl"`
  and the new form renders correctly mirrored with no layout breakage.
- Every new test mutation-tested (guard temporarily removed/broken, test
  re-run, confirmed a genuine failure with the real assertion message, fix
  restored, tests green again) — not tautological.

## Independent review

Different-model subagent (Opus), full independent re-verification (own
build/vet/test/guard run, own reading of `eod_api.go` for precedent-fidelity,
own targeted concurrency/race analysis). Found the mechanical copy of the
EOD pattern faithful and the tests genuinely load-bearing, but identified
**three blocking findings**, all stemming from the same root cause: `Apply`
has a far larger blast radius than generating a Z-report, and two of the
EOD pattern's assumptions (unbounded catch-up is harmless; only one caller
ever exists) don't survive the transfer.

- **BLOCKING, fixed — schedule bypassed on boot.** `autoUpdateDue` copied
  `eodDue`'s unbounded "any time >= hhmm today" catch-up. For EOD that's
  correct (a late Z-report is harmless); for auto-update it defeats the
  entire point of scheduling a time — a till switched off overnight and
  booted at opening time was **due** the moment it booted, so a shop
  scheduling 03:00 to avoid disrupting trading would instead see the till
  restart itself minutes after opening, with no warning. Fixed: bounded the
  window to `[hhmm, hhmm+30min)` (`autoUpdateWindow`, `update_api.go`) — miss
  the window and it waits for tomorrow's, rather than firing whenever the
  till next happens to be on.
  **Re-verified with real fail→pass evidence**: added a regression case
  (`"window expired — booted hours after the scheduled time must NOT
  trigger"`), reverted just the window bound (`elapsed >= 0` only), reran —
  genuinely failed; restored, reran, green.
- **BLOCKING, fixed — unattended restart destroys an in-progress sale.**
  `selfupdate.Apply`'s re-exec (`syscall.Exec`) is an immediate process-image
  replacement; the live basket is in-memory only (`pos.Service`, not
  persisted — only *held* sales are). The manual path is a deliberate human
  act behind an `hx-confirm` warning the till restarts; the scheduled path
  had no such consent and no guard, so combined with the first finding a
  cashier could lose a scanned basket mid-sale with zero warning. Fixed:
  `autoUpdateTick` now checks `d.Engine.Basket().ItemCount() > 0` and defers
  (without marking the day's attempt, so it retries once the sale clears,
  still bounded by the window) rather than firing through an active sale.
  **Re-verified**: added `TestAutoUpdateTick_SkipsWhenBasketHasItems`,
  reverted the guard, reran — genuinely failed; restored, reran, green.
- **BLOCKING, fixed — no serialization between manual and scheduled
  `Apply`.** `internal/selfupdate` had no lock at all; until this diff only
  one caller (the manual button) ever existed. The binary-swap sequence
  (`os.Rename(exe, bak)` → `os.Remove(bak)` on the *next* call →
  `moveFile(newBin, exe)` with a rollback via `os.Rename(bak, exe)`) has no
  atomicity across steps — if a manager clicks "Update now" while a
  scheduled tick is mid-`Apply`, one caller's `os.Remove(bak)` can delete the
  other's only backup, and a subsequent rollback then finds nothing,
  potentially leaving the install with **no binary at all** (a bricked
  kiosk needing physical reinstall). Fixed: `selfupdate.Apply` now takes a
  package-level `applyMu` via `TryLock`, failing fast with "an update is
  already being applied" for a second concurrent caller instead of letting
  two swaps interleave.
  **Re-verified**: added `TestApplyRefusesConcurrentRun` (holds `applyMu`,
  asserts `Apply` returns the in-progress error), confirmed it fails to
  compile/pass without `applyMu` existing, then passes after the fix; full
  `selfupdate` suite re-run green.

**Non-blocking, accepted as-is (noted, not fixed this cycle):**

- The `Settings.Set` error marking `keyAutoUpdateLastAttempt` is discarded
  (`_ = d.Settings.Set(...)`) — same as EOD's own settings writes, and a
  genuinely rare failure mode (local SQLite write). If it ever fails, the
  tick would retry every 30s until end of window rather than backing off
  further within the window; not worth a bigger change for this cycle.
- Changing the schedule doesn't clear `keyAutoUpdateLastAttempt` — a manager
  who reschedules after today's attempt already ran gets no feedback until
  tomorrow. Minor UX polish, not a correctness or safety issue.
- `web/ui/pages/settings.html`'s new form has no client-side error feedback
  on a 400/403 (`hx-swap="none"`, no `hx-on::after-request`) — consistent
  with the EOD form it mirrors, not a regression this diff introduces, and
  the settings page's better-pattern siblings (`#settings-save-error`) were
  not retrofitted onto either form in this pass.
- `internal/app/app.go`'s shutdown-drain comment: updated in this pass to
  list `StartAutoUpdateScheduler` alongside the other unjoined-on-`ctx`
  loops (ut-docs#153, pre-existing, out of scope to fix) — flagged as
  mattering more here since this loop can be mid-binary-rename at shutdown,
  not just mid-DB-write; noting the comment fix landed, the underlying
  drain gap itself is still ut-docs#153's to fix.
- `autoUpdateSupported()`'s check in `autoUpdateTick` is redundant with
  `Apply`'s own internal `Supported()` check — harmless, kept because it
  keeps a doomed-to-fail tick cheap (skips the `CheckNow` network call too).
- Dev builds (`buildinfo.Version == "dev"`) are always "newer" per
  `updates.Newer` — pre-existing, now reachable unattended; not something to
  fix in this card.

## Verdict

**Safe to merge after fixes.** Independent review found the EOD-pattern
mechanical copy faithful and the original test suite genuinely
non-tautological, but caught three blocking findings specific to `Apply`'s
larger blast radius versus a Z-report — an unbounded catch-up defeating the
schedule's purpose, an unattended restart with no basket-state guard, and no
serialization against the pre-existing manual-update path. All three fixed
in this same pass, each with genuine revert→fail→restore→pass evidence, not
asserted. Full gate (build/vet/test/guards) green after every fix.
