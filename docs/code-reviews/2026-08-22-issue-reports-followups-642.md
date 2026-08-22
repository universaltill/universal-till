# cloudsync/my-reports: skip no-op fail-state writes, prioritize pending under cap (ut-docs#642)

## What shipped

Two non-blocking follow-ups from ut-docs#637's review (universal-till PR
#316), tracked as ut-docs#642:

1. **`internal/cloudsync/issue_reports.go`** — `uploadPendingIssueReports`
   did an fsync'd `meta.json` rewrite via `issuereport.RecordUploadFailure`
   on every failed upload tick, forever, with `UploadFailCount` growing
   unbounded — pure write churn on an unregistered till (a 2-minute tick,
   indefinitely), since `/my-reports` already flags `not_registered`
   immediately regardless of count. Fixed: skip the write once the reason
   is unchanged **and** the bundle is already presented as failing to the
   operator (`not_registered` → failing from the very first failure;
   `other` → failing only once `UploadFailCount` reaches
   `issuereport.UploadFailingThreshold`). Still records the moment the
   reason genuinely changes (e.g. `not_registered` → `other` once
   enrolment finishes and a fresh failure happens).
2. **`internal/pages/my_reports_page.go`** — the `/my-reports` 100-row cap
   was applied to the merged sent+pending row list via a naive
   newest-captured-first truncation, so a till with >100 historical sent
   reports plus an old, long-failing pending bundle could have the failing
   row silently truncated away — hidden exactly when it matters most.
   Fixed: sort sent and pending rows separately, reserve cap room for
   pending rows first (a healthy till has none pending, so this normally
   costs nothing), then fill remaining capacity with the most recent sent
   rows, then merge back into one newest-first list for display (same
   final ordering contract as before).

No UI-visible surface change — no new strings, no template edits, no new
page — so no i18n/help-manual/screenshot follow-up applies.

## Independent review

Spawned via `Agent` at **Sonnet, fresh context, isolated worktree** (per
this card's `complexity:easy` routing — Sonnet built it, a fresh-context
Sonnet instance reviewed it independently, per the `reviewer` skill's
easy-tier exception).

**Verdict: yes, safe to merge — no blocking or should-fix findings.**

The reviewer independently re-verified both TDD claims via a live
revert → run → restore → run cycle (not taken on the implementer's word):

- Reverted the write-skip logic in `issue_reports.go` back to the old
  unconditional write:
  `TestUploadPendingIssueReportsUnregisteredSkipsWriteAfterFirstFailure`
  failed with `UploadFailCount = 10, want 1`;
  `TestUploadPendingIssueReportsOtherFreezesCountAtThreshold` failed with
  `UploadFailCount = 10, want 5`. Restored; both passed again.
- Reverted `my_reports_page.go` back to the old single-`rows` naive
  truncation: `TestMyReportsPage_FailingRowSurvivesCapOverOldestSentRow`
  failed (the failing/pending row was silently dropped). Restored; passed
  again.

It also traced the edge cases explicitly: the `alreadyPresentedAsFailing`
gate correctly still records a write the moment the reason changes in
either direction (`not_registered` ↔ `other`), since the reason-equality
check fails first; the `other`-reason threshold boundary freezes the count
at exactly `UploadFailingThreshold`, not one off; `pendingRows == rowLimit`
(sent gets zero capacity), zero pending, zero sent, and
`pendingRows > rowLimit` (capacity clamped to 0, no negative-slice panic)
were all checked and are all handled correctly. Confirmed no
repository-pattern/offline-first/kiosk-isolation/money/i18n rule is
implicated (no SQL text added outside `internal/data`/`internal/db`, no
raw file I/O in either touched function, no `web/`/locale files touched),
and no secret-shaped literals or demo client/shop names in the diff.

## Verified beyond automated tests

- `gofmt -l` on all four touched files — clean.
- `go build ./...`, `go vet ./...` — clean (run independently by both the
  implementer and the reviewer, in separate checkouts).
- `go test ./internal/cloudsync/... ./internal/pages/...` — all packages
  green, both before and after the reviewer's own TDD re-verification.
- `go test ./...` (full suite, plain, no `-race`) — green, matching this
  repo's actual `CLAUDE.md`/CI gate (`.github/workflows/ci.yml` never
  passes `-race`; `internal/plugins` is deliberately excluded from the
  main `go test ./...` step and run separately with a 20-minute timeout,
  per ut-docs#643/#753/#776 — a well-documented pre-existing timeout-margin
  issue, unrelated to this diff).
- A supplementary local `-race` run (beyond what this repo's own gate
  requires) hit that same documented margin issue in `internal/plugins`
  (unrelated `wasm_tcp_test.go`) and, once run scoped to just the touched
  packages, also hit it in `internal/pages` (unrelated
  `sync_admin_test.go`, `TestSyncAdminAPI_FullBundleThenUnchangedOnMatchingFingerprint`)
  — both 600s-timeout goroutine-dump false alarms, not real hangs, per the
  same mechanism documented in `docs/code-reviews/2026-08-16-plugins-race-timeout-margin-643.md`
  and `docs/code-reviews/2026-08-20-pages-race-timeout-margin-648.md`.
  Neither touches `cloudsync/issue_reports.go` or `pages/my_reports_page.go`.
  Not investigated further here — out of scope for this card, already
  tracked.
- All CI-blocking guards from `CLAUDE.md`'s "Before committing" list run
  clean locally: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` (`guard-docs-shots.sh` not applicable — no
  screenshot-affecting UI change in this diff).

## Explicitly deferred / not in scope

- The `internal/plugins`/`internal/pages` `-race` timeout-margin flakes
  noted above — pre-existing, already tracked (ut-docs#643/#753/#776/#648),
  unrelated to this diff's files.

## Safe to merge

Yes. No findings from the independent review, both TDD claims
independently re-verified with the exact expected fail/pass symptoms, and
the full gate (this repo's own standard, matching CI) is green.
