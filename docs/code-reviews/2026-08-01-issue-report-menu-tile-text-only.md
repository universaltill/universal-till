# Code review: issue-report menu tile + typed-only reports (voice optional)

**Date:** 2026-08-01
**Scope:** `internal/cloudsync/issue_reports.go`, `internal/issuereport/bundle.go`,
`internal/pages/issue_report_page.go`, `internal/pages/menu_page.go`,
`web/locales/{ar,en,fa,tr}.json`, `web/ui/pages/report_issue.html`
**Ticket:** universaltill/ut-docs#200 (Phase 1 — the photo/video evidence-
capture and cross-page "evidence mode" parts of #200 were already split
off into follow-up Backlog cards #211/#212 by a prior grooming pass; this
diff is the remaining scope: make "Report an issue" reachable from the
top menu, and make the voice recording optional).

## What shipped

- New manager-gated 🐞 "Report an issue" tile in the persistent ☰ Menu
  launcher (same gating pattern as the existing `/users`/`/translations`
  tiles), reachable from every page instead of only linked from Settings.
- `issuereport.Save`/`Bundle`, the `/api/issue-reports` handler, and the
  `cloudsync` upload step now treat the voice recording as optional — a
  manager can submit with just a typed note (note-or-audio required, not
  audio-always), matching the screen recording's existing optional
  behavior.
- All four locale files updated consistently (`issuereport.voice_required`
  → `issuereport.description_required`); RTL unaffected (no new/changed
  CSS).

## Independent review (different model, opus subagent)

Found one **blocking** issue before merge: the commit message (this
branch was picked up mid-flight from a prior, died cold-context cycle)
claimed "paired with a matching ut-cloud change so a note-only report
doesn't get permanently stuck retrying an upload the cloud side would
otherwise always reject" — **that change did not exist**. `ut-cloud`'s
`/v1/stores/issue-reports` handler 400'd any upload without an `audio`
part, and `IssueReport.audio_blob_key` was a non-optional ent field. Left
as-is, every note-only report this diff enables would have been rejected
by the cloud and retried forever on the till's 2-minute cloudsync tick —
the exact capability the diff claims to add would have silently never
worked, while poisoning the till's 50-entry problems digest with retry
warnings.

**Fixed pre-merge**, same session: see the paired `ut-cloud` review record
(`ut-cloud/docs/code-reviews/2026-08-01-issue-report-audio-optional.md`)
— handler + ent schema made audio-optional, a note-only report now lands
`status: ready` immediately (nothing to transcribe) instead of `received`
(which would otherwise sit forever in the background transcription
pass's query). Both sides verified together: a real note-only bundle
posted from a running till binary against a running `ut-cloud` handler-
level (httptest) stack round-trips correctly.

Also found and fixed two **nice-to-have**s:
- `internal/pages/issue_report_page.go`: the note is now trimmed before
  the "description required" check *and* before storage (previously
  validated trimmed but stored raw — a browser already trims, so this
  only mattered for non-browser clients or literal trailing whitespace).
  New regression test: `TestIssueReportAPI_TrimsNoteBeforeStoring`
  (TDD: reverted the trim, confirmed the test fails with the untrimmed
  note stored and the exact `Meta.Note` mismatch message, restored,
  confirmed green).
- `report_issue.html`'s client-side `!voiceBlob` check is falsy-unsafe
  for a zero-byte `Blob` (a `Blob` object is always JS-truthy) — noted as
  low-risk/no-bad-data (server still 400s correctly) and left as-is,
  matching the pre-existing pattern; not fixed to avoid scope creep on a
  cosmetic UX edge case.

One item noted as **out of scope, pre-existing**: `meta.json`'s `logs`
serialize with PascalCase keys (`logging.Problem` has no JSON tags) — a
local on-disk file, not a wire response, untouched by this diff.

## TDD re-verification (independent reviewer, both mutation-tested)

- `TestSaveAcceptsNoteOnly`: reverted `bundle.go`'s audio-optional guard
  back to the old "audio capture is required" error → test fails with
  exactly that message → restored → passes.
- `TestUploadIssueReportOmitsAudioWhenAbsent`: reverted the `AudioPath !=
  ""` guard in `cloudsync/issue_reports.go` back to an unconditional
  `attachFile` → test fails with `open : no such file or directory`
  (empty path) → restored → passes, along with all six other
  `TestUploadIssueReport*` cases.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- Full `go test ./...` — one failure, **pre-existing and environmental,
  not a regression**: `TestSaveCleansUpDirectoryOnWriteFailure` (a
  read-only-directory write-failure test) fails identically on
  `origin/main` in this sandbox because tests run as root (`uid=0`),
  which bypasses the `0500` directory's write restriction the test
  depends on. Confirmed by running the same test against `main`'s
  unmodified `internal/issuereport` in isolation — same failure, same
  line. Will pass on a real (non-root) CI runner.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — both clean.
- Real running server (`UT_AUTH=off`, isolated data dir): `GET /menu`
  shows the tile with its icon; `GET /report-issue?lang=fa` renders
  `dir="rtl"` with the translated required-description message; `POST
  /api/issue-reports` with `note` only → 200 + new bundle id; empty
  `note` → 400 with the new message. Server killed at the end of the
  check.
- No real client/shop name or secret-shaped literal anywhere in the diff
  (checked as part of the independent review).

## Deferred / follow-up

- Photo/video evidence capture and cross-page "evidence mode" — already
  tracked as ut-docs#211 and #212, not this card's scope.
- `report_issue.html`'s zero-byte-Blob truthiness edge case (noted above).

## Verdict

Safe to merge — cross-repo dependency (this PR + the paired `ut-cloud`
PR) closes universaltill/ut-docs#200.
