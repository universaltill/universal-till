# Code review: exit-to-os audit-log entry (ut-docs#616)

## What shipped

`POST /api/settings/exit-to-os` (added in ut-docs#608, given real effect on
Linux by ut-docs#882/PR #450, both already merged) is a manager-PIN-gated
action that breaks kiosk/fullscreen lockdown. It previously discarded the
`approver` returned by `AuthSvc.AuthorizeManager` and recorded no audit
trail — harmless while the underlying `WindowController` hook was a no-op
stub, but a real accountability gap once #882 gave it a live effect: an
operator could be pulled out of kiosk mode with no record of which manager
PIN authorized it.

`internal/pages/settings_page.go`: capture `approver` from
`AuthSvc.AuthorizeManager` (was `_, err :=`), and after `wc.ExitToOS()`
succeeds, write an audit entry via the existing `(*data.POSRepo).InsertAudit`
— `actor_id = approver.ID`, `entity_type = "settings"`, `action =
"exit_to_os"` — best-effort (logged, not fatal) matching this file's own
`settingsAudit` posture. No audit entry on a blank/wrong/locked-out PIN
(those paths `return` before reaching the insert), matching how other
manager-gated actions in this codebase don't audit-log the failure itself.
Also refreshed a stale comment above the handler that still said the hook's
"cost today is zero (a no-op stub)" — no longer true since #882.

`internal/pages/settings_page_test.go`: extended `TestExitToOSEndpoint` to
assert zero `exit_to_os` audit rows after the blank-PIN and wrong-PIN
attempts, and one row with `actor_id = "mgr1"` (the PIN-holding manager,
distinct from the `cashUser` session identity making the request) after a
successful attempt.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → same-tier review per the
scrum-master skill's model routing), isolated worktree. Verdict: **safe to
merge, no blockers.**

Checked and confirmed independently:
- Audit written only on `wc.ExitToOS()` success — no path from a failed PIN
  reaches the insert (test-verified: 0 rows after 2 failed attempts).
- `approver.ID` is the authorizing manager, not the session/cashier user —
  `AuthorizeManager` independently validates the PIN and returns that user;
  the test seeds a cashier session (`cashUser`) making the request and a
  separate manager (`mgr1`) as the PIN holder, and asserts the recorded
  `actor_id` is the manager's.
- Audit-insert failure is logged, never fails the already-succeeded
  response (no `return` after the `InsertAudit` call).
- No nil-pointer risk: `AuthorizeManager` returns `User` by value, never a
  nil pointer, on the success path.
- Repository pattern respected — zero new SQL text; calls the existing
  `(*data.POSRepo).InsertAudit` method. `guard-data-access.sh` passes.
- No i18n key needed — verified `internal/pages/audit_page.go` and
  `web/ui/pages/audit.html` render the raw `action` string with no
  per-action i18n mapping table anywhere in the audit subsystem (every
  other action, e.g. `till_promoted`, `login_locked_out`, is equally
  unmapped). `guard-i18n.sh` passes.
- No user-manual update needed — this is an invisible backend change (no
  new/changed UI); `web/help/en/display.md` already documents the
  Exit-to-OS control's behavior from #882. `guard-help-topics.sh` passes.
- No real client/shop name, no secret-shaped literal, in the diff.
- The two recurring bug classes this pipeline watches for (missing
  `os.MkdirAll` on a file-write path; a cwd-relative path instead of
  `paths.Data(...)`) don't apply — this diff writes no files.

**TDD claim independently re-verified**, not just taken on trust: reverted
only the production-code half of the diff (kept the new test), reran
`TestExitToOSEndpoint` — failed with a real assertion error
(`exit_to_os audit entry not found: sql: no rows in result set`), not a
compile error. Restored the fix — passed again.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/...` (whole package, not just the touched
  tests) — clean, no collateral breakage.
- `bash scripts/ci/guard-data-access.sh` — clean.
- `bash scripts/ci/guard-i18n.sh` — clean.
- `bash scripts/ci/guard-help-topics.sh` — clean.
- No UI/visible surface touched by this change itself (pure backend
  audit-log write) — the Tester skill's visual-check section (theme/RTL/
  longest-locale checks) does not apply to the code change.
- `guard-docs-shots.sh` initially failed on push: it hashes the *whole*
  `settings_page.go` file for any file registering a screenshotted route
  (ut-docs#620's documented, accepted over-inclusion — this diff changed
  zero pixels), so touching this file at all forced a `make docs-shots`
  regen. Ran it (84/84 shots, all 21 topics × 4 locales); 6 of the 84
  images picked up incidental non-deterministic diffs unrelated to this
  change (dynamic timestamps in the `alerts`/`designer`/`translations`
  screenshots, plus the pre-existing Chromium-version-vs-pin drift noted
  by the script itself, ut-docs#622) — actually looked at the two biggest
  diffs (`en/alerts.png`, `en/designer.png`): both render cleanly, no
  layout regression, differences are exactly the visible clock/date
  strings. Committed the regenerated manifest + images alongside the code
  change.

## Deferred / out of scope

Nothing deferred. The one nit the independent review raised (a stale "cost
today is zero" comment) was fixed in this same diff rather than filed
separately, since it was a one-line, zero-risk accuracy fix directly
adjacent to the code this card touches.

## Safe-to-merge verdict

**Yes.** Merged via `merge_method: merge` (never squash/rebase, per
ut-docs#250 — preserves real commit authorship).
