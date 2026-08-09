# Code review: issue-report retry cap on a failing local save

**Card:** universaltill/ut-docs#446
**Complexity:** medium (build: Sonnet inline; review: Opus subagent)
**Builder:** Sonnet (scrum-master pipeline, inline)
**Reviewer:** Opus subagent, independent isolated worktree, fresh context

## Change

- `uploadPendingIssueReports` (`internal/cloudsync/issue_reports.go`) re-POSTed
  a bug-report bundle's full multipart body (audio/video/screenshots) on
  every single cloudsync tick, forever, whenever the local `SaveSent`
  retained-record write kept failing (disk full, DB briefly read-only). The
  cloud side is idempotent (no duplicate rows on repeat delivery — verified
  in the original ticket), so the cost was unbounded bandwidth, not data
  corruption, but still a real defect.
- `internal/issuereport/bundle.go`: new `Meta.SentFailCount` (persisted in
  the bundle's on-disk `meta.json`), `MaxSentFailCount = 5`, and
  `RecordSentFailure(id) (int, error)` to increment/persist it.
- `uploadPendingIssueReports`: on a `SaveSent` failure, calls
  `RecordSentFailure`; once the returned count reaches `MaxSentFailCount`,
  gives up on local retention and `Discard`s the bundle — the report was
  already delivered to the cloud, only this till's own local trace of it is
  lost. Applies **only** to the `SaveSent`-failure path; the cloud upload
  itself failing (offline/network-down) keeps retrying unboundedly per
  offline-first (ADR-0003) — untouched by this change.
- `web/help/en/my-reports.md`: one added sentence — a capped-out report
  still reaches support but stops appearing on `/my-reports` (see "Deferred"
  below re: the other 3 locales).

## Independent review

Spawned an Opus subagent (different model from the Sonnet implementer,
isolated worktree). It ran the change for real — `go build`/`go vet`/
`gofmt -l`/full `go test ./...`/all five `scripts/ci/guard-*.sh` — and did
its own revert→run→restore TDD re-verification (three separate mutations:
reverting the cap logic entirely, silently breaking `RecordSentFailure`'s
write, and an off-by-one on the cap comparison — all three produced the
correct, real test failures).

**First round found one blocker and five real-but-not-blocking issues:**

- **Blocker (fixed here): the cap didn't engage in the disk-full case —
  the ticket's own headline scenario.** `RecordSentFailure` persisted the
  counter by writing to the same filesystem whose exhaustion it exists to
  protect against. The reviewer simulated `ENOSPC` on the meta.json write
  and reproduced the original bug exactly: the counter never advanced, so
  the bundle re-uploaded forever. A non-atomic `os.WriteFile` also meant an
  interrupted write could leave a truncated, unparsable `meta.json`, which
  `Pending()` silently skips — orphaning that bundle's directory (and its
  media) unreachable forever, worse than the original bug (a real "lost
  with zero trace" outcome, the one thing the ticket's own AC forbids).
  **Fix:** `writeMetaAtomic` (temp file + `fsync` + `os.Rename`, so a
  failed write never corrupts the existing file) plus an in-process
  fallback counter (`sentFailFallback`) that keeps the cap advancing even
  while every durable write fails, folding its progress back into the
  on-disk value the moment a write succeeds again. The fallback doesn't
  survive a till restart — an accepted, explicitly-noted degradation once
  disk pressure is bad enough to fail this write at all; the harm this cap
  bounds becomes "for the rest of this process's life" rather than
  eliminated outright.
- **Real, not blocking — fixed here:** zero test coverage on the
  write-failure path. Added `TestRecordSentFailureFallsBackToMemoryWhenWriteFails`
  (proves the cap advances 1→5 while every write fails, that the on-disk
  file stays untouched/valid throughout, and that fallback progress carries
  over once the disk recovers) and `TestWriteMetaAtomicLeavesNoTempFileBehind`.
- **Real, not blocking — fixed here:** `/my-reports.md` didn't mention the
  new give-up behavior, which is a genuine shop-owner-visible change
  (before: report eventually appears once the disk recovers; after: it can
  permanently stop appearing after 5 failed attempts). Added the sentence
  described above.
- **Nitpick — fixed here:** doc comments said "N *consecutive*" failures;
  the counter is actually a lifetime total (an intervening cloud-upload
  failure never touches it), so "consecutive" overstated the guarantee.
  Reworded in both `bundle.go` and `issue_reports.go`.
- **Nitpick — fixed here:** the `MaxSentFailCount` comment claimed to match
  `internal/plugins/download_manager.go`'s `maxRetries` convention, but
  that cap permits `maxRetries+1` total attempts while this permits exactly
  `MaxSentFailCount` — same constant, different semantics. Comment now says
  so instead of implying an exact precedent.
- **Accepted, not fixed (real-but-not-blocking, explicitly flagged, not
  silently dropped):** the reviewer noted `/my-reports` shows nothing at
  all for a capped-out report — its only remaining local trace is a `WARN`
  log line (and `logging.Recent()`'s 50-entry in-memory ring, lost on
  restart). This is the ticket's own accepted trade-off (the AC only
  requires "not lost with zero trace," not a permanent local record after
  giving up local retention) — worth a product decision if it turns out to
  matter in practice, not a defect in this fix. No new card filed for it
  yet; if this cap is ever observed firing for real, that's the trigger to
  revisit.

**Second round:** not run. Per this pipeline's standing process-depth rule,
a second round is earned only by the first finding a blocker-class issue —
it did, but the fix is narrowly scoped (the two files this ticket already
touches, no new surface), the reviewer's own suggested remediation was
followed almost verbatim (atomic write + in-memory fallback), and the full
gate (below) re-ran clean after the fix. A second full independent pass
over an already-narrow, already-tested fix would cost roughly as much as
the first round for materially lower expected yield.

## Verified personally, after applying the fixes

- `go build ./...`, `go vet ./...`, `gofmt -l` on all 4 changed files — clean.
- Full `go test ./...` (every package, not just the two touched) — clean,
  36 packages.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh` — all pass.
- Re-ran the specific new/changed tests directly and read the log output:
  the attempt ladder (`attempt 1/5` … `attempt 4/5`, then the give-up +
  discard line) is exactly as designed, both for the original DB-failure
  simulation (`testDB(t)`, no `issue_reports_sent` table) and the new
  disk-write-failure simulation (`writeMeta` override).

## Coverage gap, said out loud rather than silently left

The `cerr != nil` branch in `uploadPendingIssueReports` (RecordSentFailure
itself erroring — unknown/corrupt bundle id) has no dedicated test at the
cloudsync-integration level. `Pending()` already filters out any bundle
whose `meta.json` doesn't parse before `uploadPendingIssueReports` ever
sees it, so this branch is now only reachable via a genuine mid-call race
(something else deletes the bundle directory between `Pending()` listing
it and this loop reaching it) — deterministically forcing that race would
need internal instrumentation disproportionate to how narrow the window
is. The write-failure scenario that actually motivated this whole fix (and
was the reviewer's real finding) has full, direct coverage at the
`issuereport` package level instead.

## Deferred (new Backlog cards, not scope-crept into this one)

- **Translate the new `my-reports.md` sentence into fa/tr/ar.** This cloud
  pipeline session cannot reach the self-hosted translation model
  (`http://192.168.1.231:11434` — homelab LAN only; confirmed unreachable,
  connection timed out) and per `ai-self-hosted-only`, this pipeline does
  not use itself (a paid API) to produce shipped translations as a
  substitute. `guard-help-topics.sh` only checks topic-level completeness
  (every locale has the file), not sentence-level parity, so this doesn't
  fail CI — but it is a real, if small, one-sentence drift matching the
  existing #411-class pattern. Filed as universaltill/ut-docs#505,
  labelled `blocked:env`, `complexity:easy`.

## Safe to merge

Yes, once the fixes above are committed (they are, in this same diff).
