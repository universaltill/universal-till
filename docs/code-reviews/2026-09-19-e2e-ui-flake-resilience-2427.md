# Code review: harden UI E2E suite against CI-parallelism timing flakes (ut-docs#2427)

**Date:** 2026-09-19
**Author (Dev):** Scrum Master pipeline, Sonnet, inline (complexity:medium)
**Reviewer:** independent Opus subagent, isolated worktree, fresh context
**PR:** universal-till (branch `fix/2427-e2e-ui-flake-resilience`)

## What shipped

`e2e.yml`'s push/PR-triggered "UI E2E" workflow (4 parallel Playwright
workers since ut-docs#2345, each booting its own full till server +
Chromium) flaked twice in a row on an unrelated, already-merged PR's new
`main` head — 5 distinct failures across two attempts, none touching
files that PR changed, no flake in the prior 9 runs on this workflow.

Two independent, targeted fixes, both in `universal-till/e2e/`:

1. **Navigation-race resilience** (`net::ERR_ABORTED` / "interrupted by
   another navigation to the same URL", both seen in
   `fiscal-register-record-dialog-2186.spec.ts`): htmx's client-side
   settling after an `HX-Redirect` can still be in flight the instant a
   subsequent `page.goto()` fires. `helpers.ts`'s pre-existing
   `setOrderTypePromptMode` already had a 3-attempt retry loop for
   exactly this error class; extracted it into a shared, exported
   `gotoSettled(page, url, attempts = 3)` and routed every `page.goto()`
   in the affected spec (both its local helpers and its test bodies)
   through it, instead of leaving the pattern unapplied everywhere else.

2. **Assertion-timeout headroom** (`expect(...).toHaveCount(1)` timeout
   in `order-type-prompt-placement-2282.spec.ts`): traced the settings
   write (`POST /api/settings/order-type-prompt`,
   `internal/pages/settings_page.go`) to be fully synchronous — DB write
   then in-process `atomic.Value` cache update, both complete before the
   HTTP response — so there is no genuine server-side staleness window;
   the failure reads as CI-CPU-contention outrunning Playwright's default
   5s assertion-poll window under the workflow's own 4-worker load.
   Raised `e2e/playwright.config.ts`'s `expect.timeout` to 10s and CI
   `retries` from 1 to 2, matching `tests/e2e/playwright.config.ts`'s own
   already-more-generous precedent, and widened the test-level `timeout`
   from 30s to 45s alongside it so a near-maxed expect timeout can't
   collapse into a less diagnostic whole-test timeout.

## What was verified beyond automated tests

Real browser, real Go till server, CI-matching worker count (`--workers=4`):

- The two originally-flaky spec files: clean across several
  `--repeat-each` stress runs (2x, 3x, 4x) both before and after the
  review's follow-up fixes.
- A full single pass of the entire `default` project (592 tests):
  591/592 passed; the one failure (`held-chip-touch-target-2218.spec.ts`)
  is in a file this diff never touched and reads as a pre-existing,
  unrelated sandbox-contention flake (this sandbox has 4 cores total,
  running 4 till servers + 4 Chromium instances at once — tighter than a
  typical CI runner).
- `go build ./...`, `go vet ./...` clean; `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-e2e-fixtures-import.sh` all pass (no Go
  files touched — TS/config only).

No UI surface was added or changed (test-infra only), so the visual-check
attestation / help-manual requirements don't apply.

## Independent review findings

Verdict: **safe to merge, no blocking issues.** The reviewer independently
re-ran the suite, re-derived the settings-handler synchronicity claim from
the Go source itself, and confirmed the `tests/e2e/playwright.config.ts`
precedent (`expect.timeout: 10_000` / `retries: 2` in CI) is real.

- **Folded in:** widened the test-level timeout (30s → 45s) to keep
  headroom under the raised 10s expect timeout, and fixed a comment that
  said "retries once more" when `attempts = 3` actually allows two
  retries.
- **Split into its own card, deliberately not built here:** the review
  reproduced a *second*, distinct failure mode in
  `order-type-prompt-placement-2282.spec.ts`'s wedge-scan test — not a
  slow render, but `page.keyboard.type`'s real inter-keystroke delay
  occasionally exceeding `app.js`'s 100ms scan-buffer-reset window under
  contention, truncating the simulated barcode. Neither this card's
  timeout nor its retries fix that (they only mask it further); filed as
  ut-docs#2429 with the concrete fix (drive the test through
  `window.utScan.submit`, already exposed by `app.js`) rather than
  scope-creeping it into this PR.
- **Noted, accepted as-is:** `e2e.yml` triggers on `pull_request` as well
  as `push: main`, so the raised `retries` also lengthens worst-case PR
  feedback time, not only the rarer push-to-main case — accepted as the
  same trade-off `retries: 1` already made, just one notch further, and
  consistent with `tests/e2e/playwright.config.ts`'s own existing value
  for the PR-gating suite.

## Explicitly deferred

- ut-docs#2429 (wedge-scan keystroke-timing race) — see above.
- No change to the workflow's worker count itself (`e2e/playwright.config.ts`'s
  `workers: process.env.CI ? 4 : 2`) — reducing it would trade away the
  throughput win ut-docs#2345 was written for, and the evidence here
  doesn't show the parallelism itself is wrong, only that the suite's
  timing budget hadn't been widened to match it.

## Safe-to-merge verdict

Yes. Test-infra-only change, no application code touched, gates green,
real-browser verification substantially exceeds this pipeline's normal
bar for a flake-resilience fix.
