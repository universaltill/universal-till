# 012 — In-till issue reporter

Decision record: `ut-docs/adr/0022-in-till-issue-reporter.md` (read that
first — this doc is the implementation breakdown, not the "why").

## Goal

A manager, hitting a real bug on a real till, can capture a voice note +
optional screen recording + the till's recent logs in one panel, have it
reach a human triage queue with a transcript already drafted, and (after
one staff click) become a GitHub issue in a private repo — without ever
putting a GitHub credential on the till itself, and without ever writing
customer PII to a public tracker unreviewed.

## Phases (build and ship in this order — each is independently useful)

### Phase 1 — Till-side capture, local save, queued upload

- Manager-gated panel (same `isManagerOrAuthOff` convention as
  settings/reports/backoffice), reachable from Settings or a small
  persistent "Report an issue" affordance — not from the self-order
  kiosk surface (ADR-0022: staff-operated tills/back-office only).
- Voice: `getUserMedia({audio:true})` + `MediaRecorder` (webm/opus),
  start/stop, capped duration (e.g. 2 min) enforced client-side.
- Screen (optional, separate toggle): `getDisplayMedia` + `MediaRecorder`
  (webm), capped duration (e.g. 60s) — a manager can file a report with
  voice only, screen only, both, or neither (logs always attach).
- Logs: reuse `logging.Recent()` as-is for v1 (same 50-entry warn/error
  ring buffer already feeding the cloud Problems digest) — no new
  logging plumbing needed to ship this phase.
- On "Save report": write the bundle (audio blob, video blob if present,
  log snapshot, device/store id, till version, free-text note field) to
  local disk under the till's data dir, and enqueue it for upload. Saving
  locally must succeed with **no network required** (ADR-0022 offline-
  first decision) — the manager gets a confirmation regardless of
  connectivity.
- Upload: piggyback on the existing `internal/cloudsync` tick's retry
  cadence (same "try, and if it fails, try again next tick" pattern
  already used for directive/snapshot push) rather than a new background
  worker. POSTs the saved bundle to the new cloud endpoint (Phase 2)
  using the till's existing device/store token — no new till-side
  credential.
- Acceptance question to resolve empirically, not assumed: does
  `getDisplayMedia` work inside a kiosk-mode chromium boot cage and
  inside the desktop-shell wrapper? Test on whatever real environment is
  available; if it doesn't work in one of those, the panel should
  degrade to voice+logs only there rather than showing a broken control.

### Phase 2 — Cloud receiving endpoint + storage

- New `POST /v1/stores/issue-reports` on `ut-cloud` (matching this
  codebase's existing `/v1/stores/*` convention rather than a
  device-scoped path — the till-side upload code, shipped in Phase 1,
  already targets this exact path and sends `store_id`/`device_id` as
  multipart fields), multipart, size-capped — start conservative, e.g.
  32MB combined, given a 60s screen recording is the largest single item
  (matches the till-side `issueReportMaxBytes` cap from Phase 1),
  authenticated the same way the existing heartbeat/directive endpoints
  authenticate a device (store token) — no new auth mechanism.
- Store audio/video blobs via the existing
  `internal/platform/blob.Provider` (same streaming-SHA-256 path the
  vendor plugin-upload endpoint already uses — reused, not duplicated).
- New `IssueReport` entity: device/store id, till version, free-text
  note, blob keys, status (`received` → `transcribing` → `ready` →
  `filed`/`discarded`), timestamps.
- Idempotency: the till may retry an upload after a partial failure (same
  reasoning as the existing hash-gated catalog snapshot push) — key on a
  client-generated report id so a retried upload doesn't create a
  duplicate `IssueReport` row.

### Phase 3 — Transcription + staff review page + GitHub filing

- Async step (triggered on `received`): POST the audio blob to Whisper
  (`https://whisper.home.taskrunnertech.co.uk/asr?output=text`,
  multipart `audio_file` — see `homelab-k8s/kubernetes/apps/whisper`),
  store the transcript, move status to `ready`. No-op (empty transcript,
  not an error) if the endpoint is unconfigured, same convention as
  `internal/ai`/`internal/platform/translate`'s "disabled means no-op,
  never a hard failure" rule.
- New staff-only "Bug reports" page in `ut-cloud` (same shape as the
  existing `/ui/admin/reviews` plugin-approval queue — staff session
  gate, no separate token): list of `ready` reports (device/store, till
  version, free-text note, transcript, capped log excerpt, links to
  play/download the recordings), a detail view where staff can **edit**
  the transcript/note before anything is filed, and a "File issue"
  button.
- Cheap redaction assist (ADR-0022 §3): highlight any 13-19 consecutive-
  digit run in the transcript/log excerpt before staff review — a first-
  pass visual flag, not a claim of complete redaction.
- "File issue" calls the GitHub API (first use of a GitHub API client in
  this codebase) using the `bugreport-github-token` Key Vault secret
  (fine-grained PAT, `issues:write`, scoped to one repo only), creates an
  issue in the new **private** `universaltill/bug-reports` repo with the
  (possibly staff-edited) transcript + note + log excerpt + links to the
  stored recordings, sets `IssueReport.status = filed` with the created
  issue URL. A "Discard" action sets `status = discarded` with no GitHub
  call, for reports that turn out to be noise.

## Explicitly out of scope for v1

- Any redaction beyond the cheap digit-run highlight above — no attempt
  at automatic PII scrubbing/blurring of video frames or transcript text.
- Auto-filing without a human click — ADR-0022's whole point is that this
  one step stays manual.
- Self-order kiosk support — staff-operated tills/back-office only.
- Retention/deletion policy for old recordings and filed/discarded
  reports (ADR-0022 consequences — explicitly deferred).
- A public-repo path — everything here targets the new private repo
  only; copying a sanitized report to the public `universal-till` issue
  tracker remains a manual human action outside this tool.

## Open questions to revisit once Phase 1-2 are live

- Real transcription latency/quality on the actual Pi-class cluster
  hardware (Whisper's own README flags this as unverified) — may push
  toward a smaller/larger model size once real reports are seen.
- Whether the free-text note field should be required (forcing at least
  a short typed summary even when voice capture fails or isn't used) —
  decide once real staff usage shows whether voice-only reports are
  triageable without one.
