# Review — update apply: localized errors, raw error only in the log (ut-docs#5274)

PR universal-till#2299. Built 2026-11-03 (Opus); finished by a lane:cloud-41
PR sweep 2026-11-18 after the card hit its budget before review.

## What shipped
- `POST /api/update/apply` and `/api/update/android-install` answer with a
  message translated for the request's locale (`updateApplyErrorKey`, one
  classifier shared with `followErrorCode`); the raw error goes to the log
  only (Info for 4xx, Error for 5xx).
- `selfupdate.ErrApplyInProgress` sentinel replaces a text match.
- Status-bar Update button shows the translated reason in the shared,
  non-modal `#pos-alert` banner (`textContent` only).
- 4 new `en.json` keys + `en.meta.json`; help `en/updates.md` step 2.

## Sweep changes (2026-11-18)
- Merged `main`: dropped the branch's ar/fa/tr locale strings and
  ar/de/fa/tr help edits (ADR-0160: code PRs add English only).
- ADR-0178's `signature` follow code (added on main after this branch)
  now maps to `settings.update.error.verify_failed`, with a test.
- Locale test uses a key the tr pack already carries.
- Review finding 1: "already being applied" now answers 409 (Info log),
  not 502 (Error log); `TestUpdateApplyErrorStatus`.

## Review (independent, Sonnet; code authored by Opus)
Verdict: approve, no blockers/majors.
1. minor — busy returned 502 + Error log → **fixed** (409, Info).
2. minor — wrong PIN shows the same "enter the manager PIN" text as a
   missing PIN → **accepted**: the till's JS maps the 403 itself; API-only.
3. minor, pre-existing — base.html treats a non-JSON reply (the
   `http.Error` "manager only" 403) as success and polls 3 min; that reply
   is untranslated → **deferred → Backlog card** (filed at close-out).
4. nit — "up to date (vX)" is joined in Go, not a `%s` key → accepted.
5. nit — releases-API non-200 shown as "could not reach the update
   server" → accepted.

TDD re-verification (reviewer): with main's `update_api.go` the new tests
fail to compile (helpers absent); restored → pass. Commands: `go build
./...`, `go vet`, `go test ./internal/pages/ -run 'Update|Follow|Android'`,
`guard-i18n.sh`, `guard-i18n-meta.sh`, `guard-docs-shots.sh` — all pass.
Orchestrator ran all `ci.yml` build-job guards locally: all pass except
four that need tools absent from the sandbox (qemu-arm, catimport tags,
go1.27 deadcode, shellcheck) — CI runs those.

Pre-flight: help topic `updated` (en/updates.md); docs-shots `updated`
(surface hash only, script-only UI change); en.meta.json `updated`;
reference page `n/a` (no reference doc covers update API errors); README
`n/a` (no stale text); follow-up `deferred → Backlog card` (finding 3).
