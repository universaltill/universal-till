# e2e: split-tender-i18n-925.spec.ts's fa/RTL test leaks a basket item from settings-osk.spec.ts (ut-docs#1310)

**Card:** ut-docs#1310 — found while verifying ut-docs#1252 (payment overlay
redesign), pre-existing on `main`. **Complexity:** easy.
**Dev:** Sonnet (inline). **Review:** Sonnet, fresh-context subagent (easy-tier review).

## What shipped

One `test.beforeEach` hook added to `e2e/tests/split-tender-i18n-925.spec.ts`'s
`describe` block, resetting the server-global basket
(`page.request.post('/api/pos/reset')`) before each test in the file runs —
mirroring the existing precedent in
`e2e/tests/sale-screen-osk-scan-submit-1177.spec.ts`.

## The bug

All e2e specs in the `default` project share one live till server
(`Engine` is a server-side singleton, not per-browser-context). Running the
full suite, Playwright discovers spec files alphabetically, so
`settings-osk.spec.ts` runs before `split-tender-i18n-925.spec.ts`.
`settings-osk.spec.ts`'s "the hold-sale dialog has its own OSK toggle for
the label field" test scans a Coca-Cola into the basket, opens the
hold-sale dialog, then cancels — deliberately, per its own comment, leaving
the basket item in place because the next test in *that* file only checks
presence, not an exact count.

`split-tender-i18n-925.spec.ts`'s fa/RTL test does exact-total arithmetic
assuming a fresh, single-item (£1.20) basket. With the leftover Coca-Cola
still there, the real total is £2.40, so its £1.20 payment is correctly
rejected as an underpayment and the test's "Sale completed." assertion
never fires — a flaky, order-dependent CI failure, not a product bug.

Repro (from the ticket, confirmed before the fix):

```
npx playwright test tests/settings-osk.spec.ts tests/split-tender-i18n-925.spec.ts --project=default
```

## The fix

The ticket offered two options: reset in `settings-osk.spec.ts` after the
cancel, or give `split-tender-i18n-925.spec.ts` its own opening reset. Took
the ticket's own recommended option — the file that depends on exact-total
arithmetic owns its precondition, rather than relying on every spec that
might run before it to clean up after itself (that assumption is exactly
what broke here, and is one alphabetical reshuffle away from breaking
again against a different file).

## Independent review (Sonnet, fresh context)

Verdict: **APPROVE, no changes requested.**

- Confirmed the fix matches the ticket's own "more robust" recommendation.
- Confirmed `page.request.post('/api/pos/reset')` before `page.goto` is a
  proven pattern already used in `sale-screen-osk-scan-submit-1177.spec.ts`
  and `split-tender-underpayment-921.spec.ts` — the handler
  (`internal/pages/pos_api.go`) just resets server-side engine state, no
  page/session context required.
- **Independently re-reproduced the bug**: stashed the fix, re-ran the two
  files together, confirmed the fa/RTL test fails with the exact symptom
  the ticket describes (`payments (120) do not cover total (240)`).
  Restored the fix, re-ran, all 10 tests pass.
- Confirmed the fix doesn't mask a real product defect — the leaked state
  is a documented, deliberate test artifact in `settings-osk.spec.ts`, not
  an application bug.
- Confirmed scope is correctly bounded to this one file/ticket, and
  correctly does NOT attempt to retrofit every other spec with defensive
  resets — that systemic sweep is ut-docs#1315's separate follow-up card,
  left alone here.
- Considered and rejected also patching `settings-osk.spec.ts` (the
  ticket's other option): would just relocate the fragility to "does the
  next spec in file order get lucky" — the shipped fix is sufficient on
  its own.

## Verification

- Repro run (fix absent, via review's independent re-check): fa/RTL test
  fails exactly as the ticket describes.
- Repro run (fix present): `npx playwright test tests/settings-osk.spec.ts
  tests/split-tender-i18n-925.spec.ts --project=default` → **10/10 passed**.
- `npx playwright test tests/split-tender-i18n-925.spec.ts
  tests/split-tender-underpayment-921.spec.ts --project=default` → **4/4
  passed** (no regression to the neighboring split-tender file).
- `gofmt -l .` clean (no Go files touched), `go build ./...` clean.
- Diff is a single, 9-line, test-only addition to one file — none of the
  CI-blocking guards (data-access, kiosk-engine, i18n, compliance-claims,
  docs-shots, help-topics, …) scan `e2e/tests/**`, so none apply to this
  change.

## Note on local verification environment

The sandbox's pinned Playwright browser build didn't match the version
`@playwright/test` resolved to locally, requiring a temporary
`launchOptions.executablePath` override in `playwright.config.ts` to run
tests at all. That override was reverted before commit in both the dev and
review passes — confirmed via `git diff`/`git status` showing only the
intended spec-file change. Not a repo issue; CI's own environment isn't
affected.
