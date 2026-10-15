# Review: /tables and /promotions specs, same-URL `waitForURL` load race (ut-docs#4440)

Branch `test/4440-tables-promotions-load-race`. Author: Sonnet (Dev subagent,
lane:cloud-54, complexity:easy). Orchestrator and Tester: Opus 5.5.
Independent review: Opus, fresh context.

## What happened

- This is the follow-up from #4427's review. Seven calls on `/tables` and
  `/promotions` still awaited `page.waitForURL(<url the page is already at>)`
  after a navigating submit.
- Every one is a plain `<form method="post">` that the handler answers with
  a 303 back to the same page (`internal/pages/tables_page.go`,
  `internal/pages/promotions_page.go`). The promotions specs start at
  `/promotions?lang=de`, and both of their regexes also match that old URL.
- So each wait resolved against the OLD document and guarded nothing.

## What shipped (test-only)

- The seven calls now use the shared `clickThenReload` helper:
  - `e2e/tests/helpers.ts`: `deactivateAllTables` and `createTable`. Every
    spec that uses these helpers is covered.
  - `tables-designer-viewport-1173`: 2 calls.
  - `tables-tap-to-add-1025`: 1 call.
  - `percent-comma-2954`: 1 call.
  - `money-comma-2925`: 1 call.
- `clickThenReload`'s doc comment now also covers a plain-form 303 back to
  the same URL.

## Verification

- **Forced reproduction** (throwaway spec, not committed). The spec routed
  the `/tables` document so that a parser-blocking script, held for 1.5 s,
  sits in front of the page's inline binding script. That is the stand-in for
  a slow device. It then calls `createTable` and clicks the edit toggle at
  once.
  - With `origin/main`'s helpers, 2 of 3 runs failed. `readyState` was
    `loading` when the old wait returned, and the toggle click was dropped
    (no `editing` class).
  - With the fixed helpers, 3 of 3 passed with `readyState` `complete`.
- **Delaying only the POST reproduces nothing.** Playwright's `click()`
  already waits for the navigation it starts to *commit*. So the promotions
  read-back via `page.request.get` was not racing the server in practice.
  The real gap is between commit and load: the page's scripts have not run
  yet. The promotions specs still move to the helper, because their wait
  guarded nothing and the next step would depend on that implicit
  commit-wait.
- **Natural parallel load.** The four changed specs ran with `--workers=4
  --repeat-each=3 --retries=0`: 42 passed. The seven other specs that use
  `createTable`/`deactivateAllTables` passed 29 of 29. After the nit fixes,
  the four changed specs passed 14 of 14.
- **Guards:** every `guard-*.sh` in `ci.yml` passes locally except two that
  this container's tools break. `guard-deadcode-baseline` needs Go 1.27
  (deadcode here is built with 1.26), and `guard-shellcheck-version` finds
  no shellcheck installed. Neither depends on this TS-only diff. No Go code
  changed.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| nit | `clickThenReload`'s doc only described `HX-Redirect` record-dialog callers. | Fixed: it now also names plain-form 303s to the same URL (#4440). |
| nit | The same rationale was worded two ways in the comments. | Fixed: one wording is used everywhere. |
| minor (pre-existing) | `deactivateAllTables`'s `for(;;)` spins until the test timeout if a Deactivate keeps redirecting with `?err=`. The old `waitForURL` had the same exposure. | Accepted, out of scope. The PR does not cause or worsen it. |

The reviewer also confirmed these points:
- Every converted form is plain `method=post`. `base.html` sets
  `hx-boost="false"` on forms, and no submit listener calls preventDefault.
- Every handler path, error paths included, is a 303, so `load` always
  fires.
- No same-URL `waitForURL` is left on these pages.

## Verdict

Safe to merge.
