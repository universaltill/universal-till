# Code review: backup-restore restart trigger (ut-docs#1613)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1613
- **Repo/branch**: `universal-till`, `fix/1613-backup-restore-restart`
- **Complexity**: medium — Dev inline at Sonnet, review at Opus (independent
  fresh-context subagent, isolated worktree)

## What shipped

`POST /api/backup/restore`'s success path used to render only "Restore
staged — restart the till to finish" with no way to actually do that —
the identical dead end ut-docs#1550 (PR universal-till#806, merged) fixed
for the pairing "joined" screen. Root cause is the same: `db.StageRestore`
marks a snapshot to become the live DB, and `db.ApplyPendingRestore` only
ever runs once, before `db.Open`, at process startup.

- Reuses `internal/procrestart` **unmodified** — no changes to that
  package. New package-level seams in `backup_api.go`
  (`backupRestartFn`/`backupRestartSupported`/`backupRestorePending`) over
  `procrestart.Restart`/`.Supported`/`db.PendingRestore`, same convention
  `pairing_join.go` uses.
- New `POST /api/backup/restart-now`, gated by this file's own `deny()`
  (data_management — the same gate every other flat-denied route here
  uses), refuses with 409 unless `db.PendingRestore` is true, otherwise
  calls `procrestart.Restart()` and answers the standard envelope.
- New partial `web/ui/partials/backup_restore_staged.html`: a "Restart
  now" button + `/healthz`-poll script where `procrestart.Supported()`,
  else the platform's honest "close and reopen" instruction (reusing
  `tills.pairing.close_and_reopen` verbatim).
- i18n: `settings.backup.{restart_now,restarting,restarting_slow,
  nothing_to_restart}` in all four locales. `web/help/*/backups.md` gained
  a "Restoring a backup" section; screenshots regenerated
  (`make docs-shots`).

## Independent review — findings and disposition

Reviewed by an Opus subagent (Dev ran on Sonnet) in an isolated git
worktree, instructed to run everything itself and independently
re-verify the TDD claims via revert→confirm-real-failure→restore, not
take them on faith.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| M1 | Major | The restart trigger only ever appeared in the POST response itself — nothing re-checked `db.PendingRestore` on a plain page render, so a reload, an unrelated `HX-Refresh` (e.g. clicking "Back up now" right after), or a kiosk relaunch between staging and clicking lost the button entirely while the restore stayed silently staged on disk. The exact dead end this card exists to remove, merely narrowed to "after a reload." | **Fixed**: `registerSettings`'s page-render data now carries `restorePending`/`restartSupported` (`db.PendingRestore`/`procrestart.Supported()`), and `settings.html` renders the same partial whenever `.restorePending` is true — moved to sit *outside* the `.backups`-list branch specifically so it doesn't depend on that list being non-empty on this particular render. New test: `TestSettingsPage_ShowsRestartButtonWhenRestorePending`. |
| M2 | Major | The button's `htmx:afterRequest` handler called `waitForTill()` unconditionally — on a 409 (nothing staged, a stale/replayed click) or 403 (permission lapsed between staging and clicking) it showed "the till is restarting…", waited, got an immediate 200 from the still-running process, and redirected to `/login` — while nothing had restarted and app.js's generic error banner fired at the same time, asserting two contradictory things at once. `settings.backup.nothing_to_restart` was consequently dead text no user ever saw. | **Fixed**: `afterRequest` now branches on `ev.detail.successful` — only a real success polls; a 409/403 parses the server's own message (JSON `error` field, or the raw body for the plain-text 403 `deny()` returns) and shows it with a "✗" prefix (matching this file's existing convention), leaving the button live. `sendError` (a genuine network failure — the till actually gone) still resumes the poll, unchanged. Reviewer noted the *same* unconditional-`afterRequest` shape exists in the already-merged `pairing_wait.html` (#1550) for its manager-gated 403 case — out of scope here (different file/card), filed as **ut-docs#1620**. |
| Mi1 | Minor | The new manual section dropped #1550's own Windows caveat sentence, in all four locales, despite the card claiming to mirror #1550. | **Fixed**: appended the identical sentence (`web/help/en/multitill.md`'s own wording, and its ar/fa/tr equivalents verbatim) to all four `backups.md`. |
| Mi2 | Minor | `/api/backup/restart-now` wasn't added to `data_backup_manager_gate_test.go`'s existing cashier/manager/admin/super_admin table — the new dedicated test only covered cashier+manager. | **Fixed**: added a `"backup restart-now"` row to that table (past-the-gate here means 409, not 403 — no restore is staged in that fixture, which is itself the correct "past the gate" signal for this route). |
| Mi3 | Minor | `settings.backup.staged`'s wording ("Restore staged — **restart the till to finish**") sat directly above the new button that does exactly that — self-contradicting once the button exists. | **Fixed**: dropped the imperative clause in all four locales ("Restore staged. The current data will be kept as a pre-restore backup."); the Windows branch's `close_and_reopen` text still carries the actual instruction where there's no button. |
| N1 | Nit | `var T` sat outside the partial's IIFE — harmless today, but needless global state in a fragment injected into another page. | **Fixed**: moved inside the IIFE. |
| N2 | Nit | No comment explaining why `/api/backup/restart-now` writes no audit row, unlike every other route in this file. | **Fixed**: one-line comment (the row would live in the DB this restart is about to replace, same reasoning `restore_staged` already relies on). |
| N3 | Nit | Double-click race: harmless (the first `syscall.Exec` takes any second scheduled goroutine with it), but the button wasn't marked as such. | **Fixed**: `hx-disabled-elt="this"`. |
| N4/N5 | Nit | Redundant `hx-trigger="click"` on a `<button>`; a second nested `aria-live` on `#backup-restart-msg` inside `#restore-msg`'s own. | **Fixed**: dropped both. |

Also independently re-verified rather than taken on faith: no unauthenticated
path to the new route (read `internal/auth/middleware.go`'s full exempt
list — no `/api/backup` entry); no rate limit needed (unlike pairing's
first-boot flavour, there is no anonymous variant of this route); the
nil-`restartSupported` template case renders the safe close-and-reopen
branch rather than panicking (checked empirically through `html/template`,
not just reasoned about); the `/healthz` poll script's timing/redirect
logic matches the #1550 precedent exactly; the nature of the two changed
screenshot files as encoder noise (18 differing subpixels in an unrelated
3px region, same class already documented in #1550's own review) rather
than a real UI regression; two revert→restore TDD checks (disabling the
`backupRestorePending` guard, disabling `deny()`) each failed with a real,
specific error before being restored.

## Verified beyond automated tests

- Independent review's own worktree build/vet/lint/full-suite pass,
  reported back in full (see PR discussion).
- `go test ./...` (whole repo, no `-race`, matching this repo's own
  documented pre-commit gate) green at 87s for `internal/pages`. A
  separate `-race ./...` attempt on my own initiative timed out at 10
  minutes on an unrelated test (`TestReportsPage_TipsTabRecordFormGatedOn
  WorkerAllocationPermission`) — confirmed pre-existing to this diff
  (package-wide race-detector overhead across ~700 tests, not a
  regression) and not part of this repo's stated gate, so not blocking.
- `make docs-shots` re-run twice (before and after the review fixes);
  both times the only real content diffs were unrelated encoder noise —
  no `backups` screenshot exists in the manifest at all (the topic
  declares no `routes:`), so the new button was never expected to appear
  in any shot.

## Explicitly deferred (new Backlog card, not silently dropped)

- **ut-docs#1620** — `pairing_wait.html`'s restart button has the same
  unconditional-`waitForTill()`-on-`afterRequest` shape M2 fixed here, for
  its manager-gated route's 403 case (found during this review; out of
  scope for this card's file).

## Verdict

**Safe to merge.** Both Major findings (M1/M2) fixed and re-verified; all
Minor findings and nits folded in. Full gate green: `gofmt`, `go build`,
`go vet`, `golangci-lint` (0 issues), `go test ./...` (whole repo),
`guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
`guard-compliance-claims.sh`, `guard-page-http-error.sh`,
`guard-docs-shots.sh`.
