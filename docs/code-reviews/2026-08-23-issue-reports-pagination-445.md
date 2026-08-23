# Issue-reports status pull: bounded page loop + "more not shown" notice (ut-docs#445)

## What shipped

Till-side half of ut-docs#445 ("Issue-reports: no pagination story
end-to-end"). Companion change in `ut-cloud` (see that repo's own review
record, `2026-08-23-issue-reports-pagination-445.md`) added limit/offset
pagination + a true row count to `GET /api/v1/stores/issue-reports`,
replacing an unbounded `.All(ctx)`. This side closes the two gaps that
left on the till:

1. **`internal/cloudsync/issue_reports.go` — `pullIssueReportStatuses`**
   previously decoded the cloud's response with no size cap and made one
   unbounded fetch. Now:
   - The decode is bounded via `io.LimitReader(resp.Body, pullStatusMaxBytes)`
     (4MB) — a misbehaving/compromised endpoint can't make this buffer an
     unbounded body.
   - The fetch is a page loop (`pullIssueReportStatusesPage`, 200 rows/page,
     `pullStatusMaxPages = 10` safety cap) so a store with more pending
     statuses than one page still gets fully refreshed, not just the
     newest page's worth.
   - **Independent review finding, fixed same-cycle**: the loop originally
     terminated on "did the last page come back exactly full" — against a
     pre-#445 cloud (which ignores `limit`/`offset` and returns everything
     unbounded in one response), that heuristic misreads "the one page I
     got was ≥200 rows" as "there may be more" and burns up to 10 wasted,
     identical round trips re-applying the same statuses every tick. Fixed
     to terminate on `offset+fetched >= total`, consuming the cloud's new
     `total` field precisely — an old cloud sending no `total` decodes to
     its zero value, so the loop correctly stops after the single request
     that already contained everything.
2. **`internal/data/issuereports_repo.go`** — new `CountSent(ctx)` method
   (plain `SELECT COUNT(*) FROM issue_reports_sent`, same
   `issueReportsObs.trace/wrap` pattern as `ListSent`) — the only place
   this SQL is allowed to live (repository pattern).
3. **`internal/pages/my_reports_page.go` + `web/ui/pages/my_reports.html`**
   — `/my-reports` was hard-capped at 100 rows with no indication more
   exist. Now calls `CountSent` alongside the existing capped `ListSent`;
   when the true total exceeds `rowLimit`, a locale-aware notice ("N more
   reports not shown — …") renders under the intro line. Local SQLite
   `COUNT(*)` only — zero network calls, preserving this page's existing
   offline-first contract (a count failure is logged and skipped, never
   fatal to the page).
4. **i18n**: `issuereport.my_reports.more_not_shown` added to all four
   locale files (en/ar/fa/tr) — real translations, `%d` preserved.
   External-pack follow-up (independent review finding) opened as
   universaltill/ut-plugin-language-de#66 (real German translation — this
   pack already translates every sibling `issuereport.my_reports.*` key)
   and universaltill/ut-plugin-language-es#65 (accepted as known-untranslated
   baseline debt — this pack's whole `issuereport.my_reports.*` family is
   already untranslated, consistent with the existing gap rather than
   half-localizing one string).
5. **Manual**: `web/help/{en,ar,fa,tr}/my-reports.md` updated with a
   sentence on the new notice; `web/help/img/**` regenerated
   (`make docs-shots`, 21 topics × 4 locales) — `my-reports.png` itself is
   unchanged (the seed fixture has <100 reports, so the notice never
   renders in the screenshot fixture); `alerts.png`/`designer.png`/
   `invoices.png` churned as incidental re-shoot noise from a Chromium
   version mismatch between this sandbox's pre-installed browser (141.x)
   and the repo's pin (149.x) — not a functional change to those pages,
   `guard-docs-shots.sh`'s own surface-hash check passed.

## Independent review

Spawned via `Agent` at **Opus, isolated worktree** (this card's
`complexity:medium` routing: Sonnet built it, Opus reviewed it
independently, never having seen the Dev/Tester reasoning). Reviewed both
repos' diffs together (the till's page loop and the cloud's pagination
contract are two halves of one change).

**Verdict: SAFE AFTER FIXING one should-fix** (the lang-pack follow-up,
#2 below) — no blockers. All fixed same-cycle; see "What shipped" above
for how each landed.

Findings, and disposition:

1. **[should-fix, fixed]** Till doesn't consume the cloud's new `total`
   field, and the page-loop's `fetched < limit` terminator mis-loops
   against a pre-#445 cloud (10 wasted round trips/tick, bounded but real
   waste during any rollout window where tills lead cloud). Fixed as
   described in "What shipped" #1 — new regression test
   `TestPullIssueReportStatusesStopsAfterOnePageAgainstOlderCloud` proves
   exactly 1 request against that scenario.
2. **[should-fix before merge to main, fixed]** `en.json` gained a new
   key with no follow-up in the external `ut-plugin-language-{de,es}`
   packs — `lang-pack-drift.yml` is blocking on push to `main`, so this
   would have gone red on merge even though the PR check itself is
   advisory. Fixed: PRs opened in both pack repos (see #4 above).
3. **[nit, accepted as-is]** No upper bound on `offset` on the cloud side
   — noted in the cloud repo's own review record (this is a cloud-side
   finding); till side unaffected.
4. **[nit, accepted as-is]** No explicit ORDER BY tie-breaker on the
   cloud side for rows sharing an identical `captured_at` — same, a
   cloud-side finding; see that repo's review record.
5. **[nit, not new, no action]** `%d` in a locale string a shop's own
   locale override could theoretically drop — pointed out as a pre-existing,
   established pattern (10+ existing `fmt.Sprintf(httpx.T(...))` call
   sites), not something this diff introduced.

The reviewer independently re-verified TDD claims via live revert→run→
restore on both repos' test suites (not taken on the implementer's word) —
see the ut-cloud review record for the cloud-side cycles; on this repo's
side, the Tester step (prior to Review) had already revert-verified:
dropping the page loop back to one fetch failed
`TestPullIssueReportStatusesPagesWhenMoreThanOnePage`/`…SafetyCapStopsAfter10Pages`
with the exact expected request-count mismatch; removing the
`io.LimitReader` cap failed `…OversizedResponseFailsCleanly` (the row got
wrongly applied instead of staying untouched); stubbing `moreNotShownText`
empty failed `TestMyReportsPage_MoreNotShownNoticeWhenOverLimit`;
hardcoding `CountSent` to 0 failed `TestIssueReportsCountSentMatchesRowCount`.
All restored, all green again — re-confirmed after the two review fixes
above with a fresh full-suite run (below).

## Verified beyond automated tests

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- Full `go test ./...` (entire repo, every package, not just the touched
  ones) — green, both before and after the two review-round fixes.
- CI-blocking guards from `CLAUDE.md`'s "Before committing" list, all run
  clean: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh` (1611 keys, all four
  locales match), `guard-compliance-claims.sh`, `guard-docs-shots.sh` (21
  topics × 4 locales, fresh), `guard-help-topics.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`.
- Tester step drove `/my-reports` for real (httptest, real handler, real
  repo-layer seeding via `SaveSent` — not hand-crafted SQL) with 137 seeded
  rows: rendered HTML contained the exact string "37 more report(s) not
  shown — check back once older ones are filed or discarded." (137−100=37);
  100 seeded rows → no notice text anywhere in the body. Grepped the
  handler and its `Pending()` dependency for any `http.Get`/`http.Client`/
  `http.NewRequest` — none; offline-first contract holds.
- No real client/shop name anywhere in the diff; no secret-shaped literal.

## Explicitly deferred / out of scope

- `ut-plugin-language-es`'s pre-existing ~44-key `issuereport.*` gap
  (unrelated to this card — already accepted debt in that pack's own
  baseline before this change; this card only added the one new key
  consistently to the existing gap, not backfilled the whole family).
- The lang-pack PRs (universaltill/ut-plugin-language-de#66,
  universaltill/ut-plugin-language-es#65) are separate PRs in separate
  repos, tracked and watched independently — not merged as part of this
  PR.

## Safe to merge

Yes. Independent Opus review found no blockers; the one should-fix
(lang-pack follow-up) has PRs open in both pack repos; the other
should-fix (total-consuming loop terminator) is fixed and covered by a
new regression test. Full gate green.
