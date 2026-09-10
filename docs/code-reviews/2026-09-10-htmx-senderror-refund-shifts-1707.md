# Code review: e2e coverage for refund.html/shifts.html's htmx:sendError handlers (ut-docs#1707)

**Date:** 2026-09-10
**Card:** ut-docs#1707 — follow-up from ut-docs#1287, which added
`htmx:sendError` handlers to three forms (`reports_tab_tips.html`,
`refund.html`, `shifts.html`) but only wrote a dedicated Playwright test
for the cheapest of the three to set up (the tips form). This card closes
the gap for the other two — test-coverage only, no production code
changed.
**Author:** Dev + Tester phase, this pipeline (Sonnet, `complexity:easy`)
**Reviewer:** independent fresh-context Sonnet subagent, isolated worktree
— per `scrum-master`'s "Model routing by complexity" (easy tier relaxes
"different model" to "different instance")

## What shipped

One new file: `e2e/tests/htmx-senderror-refund-shifts-1707.spec.ts`, two
tests:

- **refund form**: completes a real cash sale (same scan → pay flow as
  `sale.spec.ts`), follows the receipt view's own "Refund" button to
  `/refund/<receiptNo>`, aborts `POST /api/refund`, and asserts
  `#refund-msg` shows the localized `designer.error.server` fallback text.
- **shifts form**: handles whichever state the shared till server is
  currently in (`#open-shift-form` or `#close-shift-form` — both target
  the same `#shift-result` + `document.body` listener), aborts
  `POST /api/shifts/**`, and asserts `#shift-result` shows the same
  fallback.

Both use id/attribute selectors (not localized text) except the final
assertion, matching `htmx-senderror-1287.spec.ts`'s own established
pattern and `watchConsole` exemption regex (extended for htmx's own
`htmx:sendError` self-logging).

## Independent review

Fresh-context Sonnet subagent, isolated worktree. Actually ran the suite
(not just read the diff):

- `go build ./...` clean.
- Both new tests pass on a clean run (3 repeats, no flakiness).
- **TDD independently re-verified**: removed `refund.html`'s
  `htmx:sendError` listener → the refund test fails with `#refund-msg`
  staying empty (a real, meaningful failure, not a timeout artifact) →
  restored, confirmed `git diff` clean, re-ran → passes. Same
  revert→run→restore sequence for `shifts.html`'s listener, same result.
- Confirmed `page.route` scoping doesn't over-match: `**/api/refund`
  doesn't catch `/api/refund/preview`'s background requests; `**/api/shifts/**`
  doesn't touch the `/shifts` page's own GET.
- Confirmed the shifts test's `if (await openForm.count())` branch handles
  either shared-server state correctly, so it can't be broken by whatever
  an earlier spec file in the same run left behind, and — since every
  aborted POST never lands — neither test dirties persistent till state
  for a later file.
- Confirmed the new spec actually runs in CI: not excluded by any of
  `playwright.config.ts`'s per-project regexes, so it's exercised by
  `.github/workflows/e2e.yml`'s unfiltered `npx playwright test` run.
- `scripts/ci/guard-e2e-fixtures-import.sh` passes.
- Diff scope confirmed as exactly the one new file (97 insertions, 0
  deletions) — no production code, no locale files.

**Verdict: SAFE TO MERGE.** No findings, blocking or otherwise.

## Checked and clean

- **Money.** Not applicable — test-only change.
- **i18n.** No new locale keys; the one localized assertion reuses the
  existing `designer.error.server` key already covered by
  `htmx-senderror-1287.spec.ts`.
- **Repository pattern.** Not applicable — no Go/handler changes.
- No real client/shop name used as test data (seeded demo catalog only).
- No secret-shaped values.
- Not a UI-surface change (test-only) — `ux-guidelines.md` checklist and
  help-manual update requirement don't apply.

## Deferred / out of scope

None — this card's acceptance criteria are fully met by the two new
tests; the handlers themselves are unchanged, already shipped and in
production per ut-docs#1287.
