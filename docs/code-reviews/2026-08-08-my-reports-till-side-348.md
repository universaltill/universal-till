# Code review: "My reports" tracking page (till side)

**Card:** universaltill/ut-docs#348 — till-side half (cloud-side half is a
companion `ut-cloud` PR, reviewed separately:
`ut-cloud/docs/code-reviews/2026-08-08-issuereports-list-endpoint-348.md`)
**Complexity:** hard (cross-repo; build model Fable, review model Opus)
**Builder:** Fable dev subagent
**Reviewer:** Opus subagent, independent worktree, fresh context

## Change

- New `issue_reports_sent` table (migration 032, append-only) + repository
  (`internal/data/issuereports_repo.go`): `SaveSent` (upsert), `ListSent`
  (newest-first), `UpdateStatus`.
- `uploadPendingIssueReports` no longer unconditionally discards a bundle
  after a successful upload — it saves a retained record first, and only
  discards the bulky media if that save succeeds (a failed save keeps the
  bundle for retry rather than losing it with no trace anywhere).
- New `pullIssueReportStatuses`, wired into `cloudsync.Tick` right after the
  upload call: GETs the companion cloud endpoint and updates local rows'
  status/GitHub link. Best-effort, same never-blocks-checkout contract as
  the rest of `Tick`.
- New manager-gated `/my-reports` page: reads only the local DB (works fully
  offline), translates every status (including a guarded `unknown` fallback
  for a status this till doesn't recognize — never renders a raw dotted
  key), shows attachment tags and a GitHub link when filed.
- New locale keys in all 4 files, new help topic (`my-reports`, all 4
  locales) cross-linked from the existing `bug-reporting` topic.

## Why

See the cloud-side review's "Why" — same feature, this is the half a shop
owner actually sees.

## Independent review

Ran personally:

- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed/new file —
  clean.
- Full `go test ./...` (not just the touched packages — this diff shares
  `cloudsync.go`, `internal/pages/init.go`, and two migration-replay test
  files) — clean.
- `guard-data-access.sh`, `guard-i18n.sh` (868 keys), `guard-help-topics.sh`
  (route coverage for `/my-reports` included) — all pass.
- **TDD re-verification (load-bearing), reverted and restored personally in
  an isolated worktree:**
  - Made the discard-guard unconditional again (old behavior) →
    `TestUploadPendingIssueReportsKeepsBundleWhenSaveSentFails` **failed**
    with the exact data-loss symptom (bundle dir gone despite a failed
    save). Restored → passes, full package green.
  - Deleted `issuereport.status.filed` from `fa.json` only →
    `guard-i18n.sh` **failed**, naming the exact missing key. Restored →
    passes. (Noted caveat: this guard enforces locale *parity*, not that
    `en.json` itself has every key a dynamically-built `issuereport.status.*`
    key might need — that coverage instead rests on
    `TestMyReportsPage_UnknownStatusRendersUnknownTranslation`, confirmed
    present and correct.)
- **Driven run**, real binary, `UT_AUTH=off`, seeded via the real repository
  (throwaway `scripts/`-rooted seed tool, deleted after, working tree
  confirmed clean): empty state (no panic), newest-first ordering, an
  unrecognized status rendering as "Status unknown"/"وضعیت نامشخص" (not a
  raw key), GitHub link appearing only when set, `fa`/RTL fully translated
  including `dir="rtl"`, the `?` help link resolving to the new topic,
  and — beyond what was asked — a note containing `<script>` and a
  `javascript:` URL both correctly neutralized by `html/template` (no XSS).
  **Not checked**: no browser/Playwright available in this environment
  (known limitation, ut-docs#364 — `make docs-shots` fails the same way
  here); real visual layout, RTL mirroring on screen, and a regenerated
  manual screenshot were not verified visually, only via rendered-HTML
  inspection.

## Findings — fixed in this pass

- **`github_issue_url` was stored and rendered unvalidated.** External input
  from the cloud, rendered as a trusted-looking "View on GitHub" link.
  `html/template` already blocks a `javascript:` value, but a plausible
  `http(s)` link to a non-GitHub host would have rendered fine. Fixed:
  `IssueReportsRepo.UpdateStatus` now only persists a URL with the
  `https://github.com/` prefix, silently dropping anything else (the status
  itself still updates). New test
  `TestIssueReportsUpdateStatusRejectsNonGithubURL`, written first, confirmed
  it would store the lookalike URL pre-fix, passes post-fix.
- **Manual overstated coverage.** `web/help/*/my-reports.md` said "every
  problem report" while `ListSent` is capped at 100 — a shop with >100
  reports would find the manual's claim false with no indication older
  reports exist. Reworded to "(the most recent 100)" in all 4 locales
  (summary + opening line) rather than removing the cap.
- **Two stale test comments** (`internal/db/barcode_seed_test.go`,
  `dead_seed_test.go`) said a rewind "re-applies only 02X" when it actually
  replays 02X through 032 (migration 032's own drop was added right above
  without updating the comment). Corrected both.

## Findings — deferred (real, not fixed here; noted rather than expanding scope)

- **Persistently-failing `SaveSent` re-uploads the full bundle every tick,
  forever** (`internal/cloudsync/issue_reports.go`). The new keep-the-bundle
  guard is the right call over losing the report, but has no retry cap —
  a stuck bundle with a large recording could re-push its full multipart
  body on every tick indefinitely (verified the cloud side is idempotent on
  `report_id` so this doesn't duplicate rows, just re-sends bandwidth).
  Worth a retry counter or discard-media-after-N-failures policy — new
  Backlog card, not fixed here (a real design decision, not a one-line fix).
- **No pagination story end-to-end.** Till caps at 100, cloud has no limit
  at all (see the cloud-side review). One combined follow-up card rather
  than two piecemeal fixes.
- **Pending (not-yet-uploaded) bundles never appear on `/my-reports`** —
  described honestly in the empty state and the manual rather than hidden,
  a scope choice for this card, not a defect. Worth a future card if it
  becomes a real support-call pattern.
- Timestamps render in UTC, not shop-local — matches existing precedent
  (the audit page does the same; no locale-aware date helper exists yet).
- `pullIssueReportStatuses`'s JSON decode has no size cap, matching an
  existing unbounded read elsewhere in `cloudsync.go` — real fix is
  cloud-side pagination (same follow-up card as above).
- English-only "screenshot(s)" pluralization nit; each other locale already
  translated the noun in its own natural (non-English-suffix) form.

## Outcome

Safe to merge as-is after the fixes above. `merge_method: "merge"` (not
squash/rebase) per ut-docs#250.
