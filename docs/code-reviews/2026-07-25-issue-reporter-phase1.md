# 2026-07-25 — In-till issue reporter, Phase 1 (ADR-0022 / spec 012)

## Context
Next item off the backlog once ADR-0020 (self-order kiosk) and ADR-0021
(physical keyboards) both shipped. Designed from scratch this session:
ADR-0022 resolves the open questions the backlog note flagged (till never
holds a GitHub credential; issues file into a new private repo, not the
public `universal-till`; filing itself needs one staff click as the
human gate against unreviewed PII), and `specs/012-issue-reporter/spec.md`
breaks it into 3 phases. This is Phase 1: till-side capture only — a
manager records a voice note (+ optional screen recording), the till's
recent warn/error logs attach automatically, and the bundle saves to
local disk regardless of connectivity, queued for upload on the existing
`internal/cloudsync` retry cadence. The cloud-side receiving endpoint
(Phase 2) does not exist yet, so uploads fail harmlessly today and
bundles simply wait — same "never blocks, always retries" contract as
the catalog-snapshot push already in that file.

## Design
- `internal/issuereport`: pure filesystem package (`Save`/`Pending`/
  `Discard`), no database — a bundle is a directory
  (`audio.webm` + optional `video.webm` + `meta.json`) under
  `paths.Data("issue-reports", "pending")`. `logging.Recent()` (the
  existing 50-entry warn/error ring buffer, already feeding the cloud
  Problems digest) is captured into `meta.json` at save time — no new
  logging plumbing.
- `internal/pages/issue_report_page.go`: manager-gated (`isManagerOrAuthOff`,
  same convention as `audit_page.go`/`backoffice_page.go`) `GET /report-issue`
  page and `POST /api/issue-reports` multipart handler.
- `internal/cloudsync/issue_reports.go`: one new step at the end of
  `Tick()`, best-effort, using the till's existing device/store token —
  no new till-side credential (ADR-0022 decision 1).
- `web/ui/pages/report_issue.html`: vanilla JS (`MediaRecorder`/
  `getUserMedia`/`getDisplayMedia`), matching this codebase's established
  convention of inline page-scoped `<script>` blocks (`catalog.html`,
  `settings.html`) rather than a new shared JS file.
- 20 new `issuereport.*` i18n keys across en/ar/fa/tr (ar/fa/tr are my
  own translations, not reviewed by a native speaker — flagged as a
  known gap, not a blocker for internal tooling).

## Independent review
Sonnet-model review (I ran as Opus), weighted toward correctness over
style, with explicit questions on the manager gate, the `issuereport`
package's concurrency/partial-write behavior, the multipart upload's
actual size bound, cloudsync wiring safety, and the frontend recorder
state machine. **Confirmed correct**: manager gate has no bypass on
either route; `Pending()` can't observe a mid-write bundle (`meta.json`
written last); `Tick()` calls are strictly sequential so the new upload
step can't overlap or panic; i18n keys are complete and consistent
across all 4 locales with no placeholder-count mismatches; save/error
button state machine is otherwise correct.

**Four real, concrete bugs found, all fixed before merge:**

1. **Unbounded disk write via multipart temp-file spill**
   (`issue_report_page.go`). `r.ParseMultipartForm(issueReportMaxBytes)`
   does not bound total request size the way the original code assumed —
   confirmed against Go's actual `mime/multipart/formdata.go`: once a
   file part exceeds the in-memory budget, the reader spills the entire
   remainder to a temp file with no further size check, so a request far
   larger than `issueReportMaxBytes` would hit the till's disk before the
   old `io.LimitReader` checks ever ran. Fixed: `r.Body =
   http.MaxBytesReader(w, r.Body, issueReportMaxBytes)` before
   `ParseMultipartForm`, closing the gap at the transport level.

2. **Silent truncation instead of rejection**. The old
   `io.LimitReader(file, issueReportMaxBytes)` would silently truncate an
   oversized single field into a corrupted, unplayable `.webm` while the
   till still reported "Saved" — a false success hiding broken data.
   Fixed with `readCappedOrReject` (reads `limit+1` bytes, errors if the
   source had more) — an oversized recording now gets a clear 400 instead
   of a silently corrupted save. Regression test: `TestReadCappedOrReject`;
   live-verified with a real 33MB upload (over the 32MB cap) → 400, no
   bundle written; a normal-size upload still succeeds.

3. **Orphaned bundle directory on partial write failure**
   (`internal/issuereport/bundle.go`). If the video write failed after
   audio already succeeded, `Save()` returned the error but left the
   directory behind — no `meta.json`, so invisible to `Pending()` and
   never reachable by `Discard()`: a permanent, silent disk leak on that
   failure path. Fixed: any failure in the new `saveBundleFiles` helper
   now triggers `os.RemoveAll(dir)` before `Save()` returns. Regression
   test: `TestSaveCleansUpDirectoryOnWriteFailure` (pre-creates the
   bundle directory read-only via an overridable `newBundleID` test seam,
   forces the write to fail, asserts the directory is gone afterward;
   skipped on Windows where directory-permission semantics differ).

4. **Frontend recorder state-machine gaps** (`report_issue.html`). A
   rapid double-click on either "Record" button during the async
   `getUserMedia`/`getDisplayMedia` gap could fire the browser's
   picker/permission flow twice, and a synchronous `MediaRecorder`
   constructor throw (unsupported codec, etc.) leaked the already-acquired
   stream (mic/screen capture indicator stays on with no way to release
   it from the UI). Fixed: a `voiceStarting`/`screenStarting` guard flag
   plus disabling the button for the async gap prevents the double-click;
   the `MediaRecorder` construction is now wrapped in try/catch that
   stops the acquired stream's tracks on failure. Also added the client-
   side duration caps spec 012 called for but the first pass omitted (2
   min voice / 60s screen, auto-stopping via `setTimeout`, cleared on
   manual stop) — closes the gap where an unbounded capture would have
   hit the server-side reject from fix #2 instead of stopping cleanly on
   its own.

## Verification
`go build ./...`, `go test ./...`, `go vet ./...`,
`bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
all green. New tests: `internal/issuereport/bundle_test.go` (save/list/
discard round trip, missing-audio rejection, oldest-first ordering,
missing-dir handling, the write-failure cleanup regression above),
`internal/pages/issue_report_page_test.go` (manager gate on both routes,
audio-required 400, successful save round-tripped through `Pending()`,
the truncation-rejection regression above).

Live-verified against a real built binary twice (before and after the
fixes): `GET /report-issue` renders 200 with real content; `POST
/api/issue-reports` with a real multipart body (audio + video + note)
saves a bundle to disk with the correct note, files, and a genuinely
captured log line in `meta.json`; a 33MB oversized audio upload (post-fix)
correctly rejects with 400 and writes nothing to disk; cloudsync's new
upload step runs without error on an unregistered till (returns before
reaching it, same as every other `Tick()` step).

**Explicitly not verified**: real browser `MediaRecorder`/
`getUserMedia`/`getDisplayMedia` behavior (no browser-automation tool in
this environment — the JS was reviewed by eye and by tracing the actual
API semantics, same caveat as ADR-0021's keypad-mapping tool review);
whether `getDisplayMedia` works inside a kiosk-mode chromium boot cage or
the desktop-shell wrapper (ADR-0022 flags this explicitly as an open
question, not assumed to work); Phase 2 (the cloud receiving endpoint)
not built yet, so the actual upload leg is untested beyond confirming it
fails harmlessly and leaves the bundle queued.
