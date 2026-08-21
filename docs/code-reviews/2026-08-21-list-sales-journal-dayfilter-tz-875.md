# Code review: TestPOSRepo_ListSalesJournal_DayFilter fixed for off-UTC hosts

**Issue:** ut-docs#875 · **Repo:** universal-till · **Complexity:** easy ·
**Built by:** Sonnet (inline) · **Reviewed by:** Sonnet (fresh-context subagent,
isolated worktree — per this pipeline's easy-tier routing, "different model"
relaxes to "different instance": an independent read that never saw the dev
reasoning).

## What shipped

`TestPOSRepo_ListSalesJournal_DayFilter` (`internal/data/pos_repo_batch8_sales_test.go`,
added by ut-docs#774/PR#417) hardcoded UTC timestamp literals
(`"2026-08-14T23:59:00Z"` etc.) and a hardcoded expected day-boundary string
(`"2026-08-15"`). The production query it exercises (`ListSalesJournal`'s day
filter) matches on `date(s.created_at, 'localtime') = date(?)` — so the test's
own assertions only agreed with production behavior when the host's local time
IS UTC. Found while independently verifying the sibling ut-docs#869 fix across
timezone extremes; confirmed pre-existing on unmodified `main` via `git stash`
(not introduced by #869's diff — `ListSalesJournal` isn't touched by it at all).

This is a **test-only** change — no production code touched. Fixed the same
way #869's own regression tests were: anchor every seeded instant on the
host's own local noon (`time.Now()`-derived, not a hardcoded literal — noon
keeps a same-day instant inside its calendar day for any real IANA offset,
-12..+14) and derive the expected day boundary via SQLite's own
`date(?, 'localtime')` control query (`b8ExpectedDay`, already used elsewhere
in this package — same helper #869 used), never a Go-side string literal.

## Independent review findings

Sonnet, fresh context, isolated worktree (revert/restore mutation testing is
unsafe on a shared checkout per ut-docs#386). Verdict: **safe to merge**, no
blockers.

- Confirmed scope: only the one test file changed (30 insertions, 10
  deletions) — no production file, no `web/help/**`, no locale file. The
  UX-guidelines checklist and manual-currency check don't apply; confirmed via
  `git diff main HEAD --stat`, not assumed.
- Confirmed no real client/shop name and no secret-shaped literal (the one
  `key`/`value`-shaped line is a pre-existing, unrelated `settings` table
  insert helper, not part of this diff).
- Independently re-verified the TDD claim by reverting the commit
  (`git revert --no-commit HEAD`), re-running under `TZ=Asia/Tokyo`, and
  confirming the pre-fix test genuinely fails (`day-filtered entries
  mismatch: [{ReceiptNo:D2 ...} {ReceiptNo:D1 ...}]` — the old hardcoded-UTC
  test drags in the prior-day sale and drops the correct one), then restored
  and confirmed green again.
- Re-ran the fix under all five verification timezones independently
  (`TZ=UTC`, `TZ=Asia/Tokyo`, `TZ=America/New_York`, `TZ=Pacific/Kiritimati`,
  `TZ=Etc/GMT+12`) — all pass.
- Full `go test ./internal/data/...` re-run clean, no other test affected.

No findings deferred; nothing outside this diff's scope was surfaced.

## Verified

- `go build ./...`, `go vet ./...` clean; `gofmt -l` clean.
- Full `go test ./...` — all 38 packages green under `TZ=UTC` (CI's zone).
- `TestPOSRepo_ListSalesJournal_DayFilter` re-verified passing under `TZ=UTC`,
  `TZ=Asia/Tokyo`, `TZ=America/New_York`, `TZ=Pacific/Kiritimati`,
  `TZ=Etc/GMT+12`.
- TDD claim re-verified independently (see above): pre-fix fails under
  `TZ=Asia/Tokyo`, post-fix passes.
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` all pass (unaffected — test-only change).
- No file-write/`paths.Data` concerns, no i18n keys, no help topic affected —
  test-only, backend-only, no user-facing surface at all.

## Verdict

Safe to merge.
