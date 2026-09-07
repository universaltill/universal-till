# Code review: catalog-image-to-till image-load assertion timeout (ut-docs#1362)

**Date:** 2026-09-06
**Branch:** `fix/1362-catalog-image-load-timeout`
**Card:** universaltill/ut-docs#1362
**Complexity:** easy

## What shipped

`ut-docs#1362` reported the uploaded-thumbnail `complete`/`naturalWidth`
assertion pair in `e2e/tests/catalog-image-to-till.spec.ts` failing
deterministically (3/3) in a sandboxed pipeline runner under the default
5s per-assertion Playwright timeout.

Re-investigation before any code change: ran the spec 3 times in
isolation (matching the original report's repro method) and once as part
of a full 329-spec e2e suite run, all in this session's own sandbox (the
same pre-installed-Chromium runner class `playwright.config.ts` already
special-cases). **All 4 runs passed clean** — the original failure is not
a standing property of this environment as a category, so there is no
live regression to reproduce-and-fix in the strict TDD sense.

Given that, shipped a defensive, cheap-insurance change rather than a
confirmed root-cause fix: bumped the four image-load assertions (catalog
thumbnail + basket-line thumbnail, both `complete`/`naturalWidth`) from
Playwright's default 5s timeout to 15s, with a comment explaining why.
The exact equality checks (`naturalWidth: 2`, `complete: true` — the
whole point of this test, proving the *new* uploaded bytes loaded rather
than the seeded 289×375 image) are unchanged.

## Independent review

Spawned a fresh-context Sonnet subagent (easy-complexity routing —
different instance, not a different model, per the `reviewer` skill's
documented exception for `complexity:easy`). It did not take the PR
description on faith:

- Diffed `main` against the branch directly and confirmed the diff
  matches the PR's claim exactly: 4 `toHaveJSProperty` calls gained
  `{ timeout: IMG_LOAD_TIMEOUT }` (`15_000`), values unchanged.
- Confirmed via `git diff --stat`/`--name-only` that exactly one file
  changed — no Go, locale, or help-topic surface touched, so the
  repository-pattern/money/i18n guards are genuinely not applicable here.
- Checked the installed `playwright@1.61.1` type definitions directly
  (`e2e/node_modules/playwright/types/test.d.ts`) to confirm
  `toHaveJSProperty(name, value, { timeout })` is a real, typed parameter,
  not a hallucinated API.
- Actually ran the test: 2 plain runs plus a 9x `--repeat-each=3` run, all
  9 executions passing, with the assertion consistently resolving in
  ~1.6–1.7s — an order of magnitude under even the *old* 5s bound.
- Reasoned through the substantive question directly: raising the timeout
  cannot convert a wrong value into a matching one, since
  `toHaveJSProperty` polls until the property equals the target or the
  timeout elapses — a genuinely broken upload (wrong path, stale
  cache-busting) still yields `naturalWidth: 0` or the stale `289`/`375`,
  never `2`, so it still fails, just slower to report. The change extends
  patience for a slow-but-correct decode; it cannot mask a real
  regression.
- Verified the commit author is the pipeline owner
  (`Farshid Mirza <4035824+farshidmirza@users.noreply.github.com>`) with
  Claude only in the `Co-Authored-By:` trailer, and scanned the diff for
  secret-shaped literals / real client names (none found).

**Verdict: SAFE TO MERGE, no blocking findings.** One disclosed
observation, not a defect: the original "deterministic 3/3" failure
remains technically unconfirmed as fixed — this ships as insurance against
recurrence, not a proven root cause, and both the code comment and this
record say so plainly rather than overclaiming.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean (change is
  test-only TypeScript, but repo-wide Go health confirmed unaffected).
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite
  green; `internal/plugins`'s slower suite skipped as unrelated to a
  test-only e2e change.
- `golangci-lint run ./...` — 0 issues.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`) — all pass (none
  logically triggered by a test-only diff, but running them costs nothing
  and confirms no accidental collateral change).
- `catalog-image-to-till.spec.ts` run personally 3x in isolation plus 1x
  as part of the full 329-spec suite before handoff, then independently
  re-run by the reviewer (2 plain runs + a 9x `--repeat-each=3` run) —
  13 total post-change executions, all green.
- No real client/shop name or secret-shaped literal in the diff (checked
  personally and by the independent reviewer).

## Deferred / out of scope

- The original failure's root cause (why it reproduced 3/3 on whatever
  sandbox instance filed the original report) remains unconfirmed. If it
  recurs, re-open with a fresh reproduction — this record's own evidence
  (4+9=13 clean runs across two independent sessions) is what should be
  weighed against any future report before assuming this fix was
  insufficient.

## Safe-to-merge

Yes. Merged via `merge_method: "merge"` (never squash/rebase — see the
`reviewer` skill's "Merge method" note, ut-docs#250) once CI is green.
