# Review — record dialog keeps status/Lock/exit-to-OS on a self-order till (ut-docs#4324)

**Branch:** `fix/4324-record-dialog-status-row-operator`
**Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable, fresh context, separate worktree

## What shipped

- `web/ui/partials/record_dialog.html`: the status row (sync chip, exit-to-OS
  link, Lock form) is always rendered. The `{{ if not selforder }}` gate is
  gone. Every page that uses the dialog (`/categories`, `/locations`,
  `/registers`, `/fiscal-register`, `/country-settings`) needs a staff
  session, so a customer at the kiosk can't reach it. The till-wide
  display.mode flag stripped Lock from a manager's maintenance sign-in on a
  satellite kiosk (#4307), and from a manager browsing that till from a phone.
- `internal/httpx`: the now-unused `selforder` template func is removed.
  `SelfOrderMode()` exposes the flag instead (it still gates the release
  notice, #3091).
- New `internal/pages/record_dialog_kiosk_4324_test.go`: fails if any
  `self_order*.html` template includes the record dialog. Customer
  containment now depends on that.
- Tests that read the flag through the dialog or the template func now use
  `httpx.SelfOrderMode()`. Comments, `CLAUDE.md`, and the ut-docs
  coding-standards §10 and list-and-dialog-pattern docs are updated to match.
- `web/help/img/manifest.json`: surface hash only (Docs-Shots-Unchanged: no
  docs-shot renders a till in self-order mode).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor, pre-existing | `releaseNoticeVersion` uses the same till-wide gate, so a manager's maintenance session never sees the post-update notice | Out of scope → ut-docs#4394 |
| 2 | nit | `settings_page.go` comment wrapped past 119 chars | Fixed |
| 3 | nit | CLAUDE.md wording read "axis is device mode" next to "staff keeps them" | Reworded |

The reviewer checked every auth-exempt route (`internal/auth/middleware.go`).
None of them renders a page that uses the record dialog. No template, plugin
or harness still calls `selforder`.

## Verified

- TDD: with the gate restored, `TestCategoriesPage_RecordDialogStatusLockExitAffordance`
  fails in "self_order till, staff session" (author, then the reviewer
  independently). With a planted `self_order_*.html` that includes the dialog,
  the new kiosk-template test fails and names the file.
- `go build ./...`, `gofmt`, `golangci-lint` (0 issues), `go test ./internal/httpx ./internal/pages`
  (full package, ok). The guards pass: i18n, data-access, no-showmodal,
  kiosk-engine, compliance, competitor, core-neutral, help-topics, help-drift,
  deadcode, inline-handlers, readme-links, docs-shots.
- e2e: the six record-dialog specs (default project) passed 43/44 in one
  parallel run. `locations-record-dialog-2124` at 1024×600 failed once under
  load, then passed 5/5 when rerun on its own.
- Driven run on the real binary: `display.mode=self_order` set via
  `/api/settings/upsert`, then the /categories Edit dialog opened. Online chip,
  exit-to-OS and Lock were visible at 1024×600 and at 360×740, and the
  screenshots were checked. `/self-order` still shows only its own kiosk 🔒
  sign-in link, with no status row. Checked in English only. There's no CSS or
  string change, so RTL and long-locale layout are unchanged from #2099.

## Verdict

Safe to merge.
