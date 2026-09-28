# Review: sell screen flat with zero categories — test backfill (ut-docs#2320)

**Date:** 2026-09-28 · **Lane:** cloud-54 · **Complexity:** easy
**Author model:** Sonnet (Dev) · **Reviewer:** Opus 5.5, fresh context

## What shipped

The owner's decision (2026-09-28) was: zero categories → quick buttons render
flat with no tab bar; one category plus uncategorised buttons → tab bar. The
behaviour itself already shipped with ut-docs#2613, which retired the All tab:
`web/ui/partials/buttons.html` has `$hasTabs := gt (len .Groups) 1`. This
change only adds the tests the card's acceptance criteria ask for:

- `internal/ui/buttons_http_test.go`: `TestButtonsHTTPList_FlatWhenNoCategoriesConfigured`
  now also asserts there is no `class="tab-bar"`. The new
  `TestButtonsHTTPList_TabBarWhenCategoryAndUncategorizedButtonCoexist`
  pins the two-tab case (the category tab plus `#cat-tab-uncategorized`).
- `e2e/tests/worker-till.ts`: `startWorkerTill(idx, { fresh: true })` boots
  an unseeded till on worker port + 50. It never reuses a running till. Its
  data dir comes from `mkdtemp`. Existing callers are unchanged.
- `e2e/tests/sell-flat-no-categories-2320.spec.ts`: drives both states in a
  real browser against that fresh till, running serially. The default
  worker tills ship four demo categories, so they can't reach a
  zero-category state.

No product code, locale keys or help topics changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Header comment said nothing touches the shared worker till, but the `./fixtures` auto fixtures still boot and reset it | Fixed: comment corrected |
| 2 | nit | Local runs: two sessions whose `UT_E2E_WORKER_PORT_BASE` values are exactly 50 apart could reuse each other's fresh till | Fixed: comment on `FRESH_PORT_OFFSET` |
| 3 | nit | Only `strip_overflow` mode is covered, since it is the only mode with a tab bar | Fixed: noted in the spec header |
| 4 | nit | No review record | This file |

No blockers and no should-fix findings. CI port analysis: 4 workers put the
regular tills on 9091–9094 and the fresh tills on 9141–9144. The server's
port fallback walks at most +20, so the two ranges never meet, and the
fixed servers use 8091–8097. Serial-mode retries get a new empty till.

## Verified beyond automated tests

- Mutation runs, repeated by the orchestrator and by the reviewer:
  - `gt (len .Groups) 2` → the new Go test and e2e case (b) fail.
  - `gt 0` / `ge 1` → the zero-category Go test and e2e case (a) fail
    (`.tab-bar` expected 0, received 1).
  - Template restored after each run.
- e2e: the new spec passes (2 tests). With the neighbouring sell-screen
  specs (1325, 2307, 2173, 1433), 20 pass.
- No test process or temp dir is left behind after failing runs.
- The `ci.yml` build-job guards pass locally, except
  `guard-shellcheck-version.sh`: shellcheck isn't installed in this
  container, and no shell files changed.

**Verdict:** safe to merge.
