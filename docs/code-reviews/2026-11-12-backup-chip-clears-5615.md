# Review: the backup-copy chip clears right after a save (ut-docs#5615)

Date: 2026-11-12 · Branch: `feat/5615-backup-chip-clears` (uncommitted diff on `origin/main`)
Author: Sonnet (Dev) · Reviewer: Opus 5.5, independent fresh context

- Card: universaltill/ut-docs#5615, "'No backup copied off this till' chip stays up for up to 10 min after Save to Downloads".
- Pre-flight: gofmt clean (Dev + reviewer on `internal/pages`); `go build ./...` ok (Dev); `go test ./internal/pages/... ./internal/manual/...` ok (Dev), `-run TestBackupCopyChip` re-run ok (reviewer); `go vet ./internal/pages/` ok (reviewer); `golangci-lint run ./internal/pages/...` 0 issues (reviewer; the Dev had not run it, and the full `./...` run is still to do before commit); guards `guard-docs-shots`, `guard-help-topics`, `guard-i18n`, `guard-fixed-dates`, `guard-no-showmodal`, `guard-rtl-logical-css` rc=0 (reviewer); the rest of `ci.yml`'s build job and `shellcheck` were not run (no shell changed). `web/help/en/backups.md` updated; no `en.json` key; `README.md` n/a; no manifest, plugin or money change; review record: this file.

## What shipped

- `internal/pages/backup_api.go`: on success only, `POST /api/backup/save-copy/{name}` (200 with the "Saved to" fragment) and `POST /api/backup/copied/{name}` (204, Android picker) set `HX-Trigger: backup-copied`. This happens after `recordBackupCopied`. On save-copy, the header is set before the first body write, so it goes out with the implicit 200. Every 400/404/500 branch returns before that line.
- `web/ui/layouts/base.html`: `#sb-backup-copy-mount` now has `hx-trigger="load, every 600s, backup-copied from:body"`. The comment explains why.
- `web/help/en/backups.md`: one clause saying the reminder disappears as soon as a copy is saved.
- `web/help/img/manifest.json`: `surface_sha256` was regenerated (base.html and a pages `.go` changed); `guard-docs-shots` passes.
- `internal/pages/backup_copy_chip_test.go`:
  - The header is asserted on save-copy 200 and picker 204.
  - The header is asserted absent on save-copy 400 (bad name) and picker 400/404.
  - A template test checks that the mount line carries `backup-copied from:body`.

## Review checks

### Does htmx act on the header at both call sites?

Checked against the vendored `web/public/vendor/htmx.min.js`, which is 1.9.12.

- `handleAjaxResponse` (minified `Mr`) handles `HX-Trigger` first, right after `htmx:beforeOnLoad`. That is before any status-code, swap or 204 handling, so a 204 with `swap:'none'` still dispatches it.
- **Settings button** (`settings.html:1117`, `hx-post` save-copy): the event fires on the button. It is a bubbling CustomEvent, so it reaches `document.body`, where a `from:body` listener sits. At that point the button is still in the DOM, because the swap into `#backup-dl-msg` happens later.
- **Android picker** (`settings.html:1184`, `htmx.ajax('POST', …, {swap:'none'})`): `ajaxHelper` (`Nr`) passes `source: undefined`, and `issueAjaxRequest` (`he`) then defaults the element to `document.body`. The event is therefore dispatched on body itself, and `from:body` hears it directly.
- On Android, the capture-phase click handler stops the htmx button, so a save fires exactly one request and one event.

### Is the preserved mount's listener kept after a boosted navigation?

- The mount sits inside `#ut-page` (boosted). With `hx-preserve`, 1.9.12 moves the live element into the incoming fragment, so it is never `cleanUpElement`'d.
- `processNode` then skips it, because its `initHash` is unchanged. Its `every 600s` poll and its body listener survive, and they are not duplicated.
- The `from:` handler's `bodyContains(elt)` check passes, because the element is back in the document.
- This is the same pattern as the existing `#notices-strip` (`notices-changed from:body`, `hx-preserve`) and `#pairing-notice-mount` (`tills-changed from:body`), which already work across boosted pages.

### Do the tests really fail without the fix?

Re-verified inline by the reviewer:
1. Deleted the two `HX-Trigger` lines and removed `, backup-copied from:body` from base.html.
2. Three tests fail with the claimed messages:
   - `:182` `save-copy HX-Trigger = "", want backup-copied`
   - `:236` `204 HX-Trigger = "", want backup-copied`
   - `:277` `… lacks "backup-copied from:body"`
3. Restored the files from byte copies. The tests pass again and `git diff --stat` is unchanged.

The save-copy failure leg reaches the handler. A probe with Go's ServeMux confirmed that `..%2F..%2Fetc%2Fpasswd` matches `{name}` as a single segment (PathValue `../../etc/passwd`), reaches the handler and gets a 400. It is not a 301 path-clean redirect, so the "no header on failure" assertion is not a false pass.

### Repo rules

- **i18n:** no new user-facing string. The event name is not user-facing.
- **RTL:** no CSS change.
- **No modal:** none added. The chip stays a status chip.
- **showModal:** not used (guard green).
- **Offline-first:** both endpoints are local; no network is involved.
- **Event name:** `backup-copied` is unique in the repo, so no other listener fires on it.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Info | The template test (`backup_copy_chip_test.go:269`) is a string match on the mount's line. It proves the attribute is there, not that htmx refreshes the chip. If someone moves `hx-trigger` onto a second line, or wraps the mount, the test reports "not found"/false red rather than a false pass, so it fails safe. The behaviour was confirmed above by reading htmx 1.9.12. An e2e (save, then the chip is gone) would pin it end to end. | Accepted |
| 2 | Info | Only one save-copy failure leg is tested: 400 on a bad name. The two 500 legs (`BackupDir` error, `saveBackupToDownloads` error, `backup_api.go:338/344`) return before the header line. That was checked by inspection only. | Accepted |
| 3 | Info | `GET /api/backup/download/{name}` also records a copy (`TestBackupCopyChip_ADownloadClearsIt`). It is a plain browser download, so it cannot carry an htmx trigger. No template links it today, so the help sentence ("disappears as soon as a copy is saved") holds for every UI path. If a direct-download link is added later, the chip would again linger up to 10 min after it. | Accepted (note for whoever adds such a link) |
| 4 | Info (pre-existing) | `guard-help-drift` warns that the `ar`/`de`/`fa` `backups` topics have 7 headings against 8 in English. That drift predates this diff, which changes one sentence and no structure. Translations come through Weblate (ADR-0160). | Out of scope |

No blocking or medium findings. The reviewer changed no code.

## Recurring-bug checks

- No file write is added, so the `os.MkdirAll` check is n/a.
- No data path is added, so the `paths.Data` check is n/a.
- No real shop names or secrets appear in the test data.

## Verdict

**APPROVE.** The change is minimal and follows the existing `notices-changed` / `tills-changed` pattern. The header is set only on the two success paths and goes out with the right status. Vendored htmx 1.9.12 dispatches it at both call sites: on the button, and on body for `htmx.ajax`. The preserved mount's body listener survives boosted swaps. The new tests fail without the fix.

Before commit: run the full `golangci-lint run ./...`, `go test ./...` and the remaining `ci.yml` build-job guards.

## Deferred

- None required. Optional: an e2e spec (Settings → Backups → Save to Downloads, then `[data-testid=sb-backup-copy]` is gone without waiting) covering finding 1.
