# Code review: bug-report upload observability (failing, not pending)

**Card:** universaltill/ut-docs#637 (split from the umbrella ut-docs#623)
**Complexity:** medium (build: Sonnet inline; review: Opus subagent)
**Builder:** Sonnet (scrum-master pipeline, inline)
**Reviewer:** Opus subagent, fresh context, two rounds (see below)

## Change

`cloudsync.pushIssueReports`/`uploadPendingIssueReports` retries a bug-report
upload forever by design (right for a merely-offline till), but that was
equally forever for a till that will **never** succeed — unregistered, or a
misconfigured cloud URL — and a bundle that never successfully uploads never
even reached `/my-reports`, since that page only read the
`issue_reports_sent` table (populated only after a successful upload). The
shop owner had no way to tell "on its way" from "this will never arrive."

- `internal/issuereport/bundle.go`: `Meta` gains `UploadFailCount`/
  `UploadFailReason` (distinct from the existing `SentFailCount` mechanism,
  which guards a *different* failure — `SaveSent`, not the upload itself),
  `UploadFailingThreshold = 5`, `UploadFailReasonNotRegistered`/
  `UploadFailReasonOther`, and `RecordUploadFailure`/`ClearUploadFailure`.
  Never capped — the upload keeps retrying unboundedly per offline-first
  (ADR-0003); the counter only decides how a still-pending bundle is
  *presented*.
- `internal/cloudsync/issue_reports.go`: `uploadIssueReport`'s "not
  registered" error is now a real sentinel (`errNotRegistered`); a failed
  upload classifies and records it; a successful upload clears it before the
  `SaveSent` step runs (so a bundle that recovers but then hits a
  `SaveSent` failure doesn't keep showing a stale reason for a report that
  did, in fact, reach the cloud).
- `internal/cloudsync/cloudsync.go`: `Tick` — see "Independent review",
  blocker #1.
- `internal/pages/my_reports_page.go`: `/my-reports` now also lists
  still-pending bundles from local disk (`issuereport.Pending()`), merged
  newest-first with the sent rows, deduped against them, capped at the same
  100-row limit. A pending bundle shows "pending" normally, and "failing"
  (with a translated reason) once it crosses `UploadFailingThreshold` —
  immediately for `not_registered`, which can't self-resolve by waiting.
- `web/ui/pages/my_reports.html`, `web/public/app.css`: new `.tag.warn`
  chip + reason line.
- `web/locales/{en,tr,fa,ar}.json`: 4 new keys per locale
  (`issuereport.status.{pending,failing,failing_reason.not_registered,
  failing_reason.other}`), plus reworded `intro`/`empty` strings (see
  "Independent review" below).
- `web/help/*/my-reports.md` (all 4 locales) + regenerated screenshots
  (`make docs-shots`).

## Independent review

Two rounds — the first found a blocker, which per this pipeline's rule
earns a second, scoped-to-the-fix round (not a re-review of the whole
diff).

### Round 1 (Opus, fresh context)

**Blocker — the whole mechanism was unreachable in production for both
cases the ticket names.** `cloudsync.Tick` called `uploadPendingIssueReports`
at its *tail*, after both the "not registered" early return and the
`pushSync` error return. So an unregistered till, or one whose
`/v1/stores/sync` call is failing, never reached the new upload call at all
— exactly the two cases in the requirement ("a till that will NEVER
succeed"). The test suite was green only because the new tests called the
unexported `uploadPendingIssueReports` helper directly, bypassing the exact
guard that made this unreachable.
**Fix:** moved the call to the very first statement in `Tick`, before both
guards. Safe unconditionally: `uploadIssueReport` self-guards on
registration internally (no network call when unregistered) and
`issuereport.Pending()` is a pure local disk read. Added two `Tick`-level
regression tests (`TestTickUploadsIssueReportsEvenWhenUnregistered`,
`TestTickUploadsIssueReportsEvenWhenSyncFails`) that drive `Tick` itself,
not the unexported helper.

**Major — no end-to-end regression test for the acceptance criterion.**
Same fix as above; the two new `Tick`-level tests are it.

**Minor, all fixed:**
- A bundle whose upload succeeds but whose `SaveSent` then fails (survives
  for retry) kept its stale `UploadFailCount`/reason from an earlier tick —
  contradicted its own doc comment. Fixed with `ClearUploadFailure`, called
  right after a successful upload, before `SaveSent` runs either way.
- `/my-reports` could render the same report twice if a successful upload's
  `Discard` failed (rare, logged, non-fatal) — deduped pending bundles
  against already-sent IDs.
- `sort.Slice` on the merged row list was unstable (equal-second
  timestamps); switched to `sort.SliceStable`.
- The merged list could exceed the page's own "most recent 100" promise
  (pending bundles added on top of an already-capped sent list); capped the
  merged list at the same limit.
- Only the *new* `/my-reports` tests isolated `issuereport.PendingDir`;
  moved the override into `newMyReportsTestMux` itself so every test in the
  file is isolated by construction (a local checkout that has ever captured
  a real bug report would otherwise leak into `TestMyReportsPage_EmptyState`
  et al.).
- The intro/empty locale strings and the manual topic (all 4 locales) still
  said reports only "appear" once they "upload" — no longer true now that a
  pending row appears immediately. Reworded in all 4 locales + help topics;
  re-ran `make docs-shots`.

### Round 2 (Opus, fresh context, scoped to the fix commit only)

Verified each round-1 item against the actual diff (not the round-1
report's claims), traced `Tick` line-by-line for any path that could still
skip the upload call, ran the full targeted test suite plus all guards, and
skimmed for anything the fix itself might have newly introduced (existing
`Tick` tests unaffected — the package-relative default `PendingDir` doesn't
exist under `internal/cloudsync`, so the new call is a true no-op for them;
no concurrency issue — `uploadPendingIssueReports` is one sequential loop).

**Verdict: ship as-is.** Three non-blocking nits noted for a possible
follow-up card (not filed as blocking this one):
- An unregistered till now does an `fsync`'d `meta.json` rewrite per
  pending bundle every 2-minute tick, forever, even though the page ignores
  the count once the reason is `not_registered` (it flags immediately
  regardless of count) — pure write churn, skippable if the reason/verdict
  is unchanged.
- The 100-row cap is applied *after* the merge and doesn't prioritize a
  failing row over an old sent one — a till with >100 sent reports plus a
  very old failing pending bundle could have the failing row truncated
  away. Low likelihood.
- The empty-state string still opens "No reports **sent**..." though the
  page now covers captured-but-unsent too (the second sentence corrects
  it) — cosmetic.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...`, full `go test ./...` — all green (ran
  twice: once before, once after the round-1 fixes).
- All five `scripts/ci/guard-*.sh` (data-access, kiosk-engine,
  plugin-menu-read, i18n, help-topics) plus `guard-docs-shots.sh` — green.
- `make docs-shots` actually run against the real pre-installed Chromium
  (ut-docs#622/#620) — confirms this environment *can* run it despite
  ut-docs#620's open report of a Playwright browser-revision mismatch; the
  guard only warns (non-fatal) about the version gap, doesn't block.
- Traced `Tick`'s control flow by hand (both review rounds) to confirm no
  code path can skip the upload call — not just trusted the passing tests.

## Deferred (filed as separate cards, not this one)

- ut-docs#638 — ut-cloud: surface a count/age of bug reports awaiting the
  human filing gate (ADR-0022).
- ut-docs#639 — pipeline cycle summary must distinguish a successful empty
  bug-report sweep from a failed one (process/doc fix to the scrum-master
  skill, not application code).

Both were split out of the umbrella card ut-docs#623 (relabelled `epic`)
during this cycle's grooming, alongside this one.
