# Review: the CSP report-only spec asserts no `script-src-attr` violation (ut-docs#3505)

Branch `test/3505-csp-script-src-attr`. Author: Sonnet (Dev subagent,
lane:cloud-54, complexity:easy). Independent review: Opus 5.5, fresh context,
in a separate worktree.

## What shipped

Only `e2e/tests/csp-report-only-2913.spec.ts` changed:

- **Settle poll.** The browser POSTs reports asynchronously, so the existing
  `length > 0` poll can return before later reports arrive. A second
  `expect.poll` re-reads `GET /csp-report` about once a second and passes once
  two reads in a row have the same unique count. It then keeps that inventory.
  The store dedups on directive, blocked URI, document, source and line, and it
  never shrinks, so an unchanged length means no new violation has arrived.
- **Assertion.** It runs after the inventory is attached, so the attachment
  still exists when the test fails. Any entry with `effective_directive ===
  'script-src-attr'` fails the test, and the message lists the offending
  entries as JSON.
- The test timeout is 60s instead of the 45s default. Two 15s polls plus 13
  navigations could exceed 45s on a slow CI runner.

## Scope of the runtime check (review finding 1)

Chromium reports `script-src-attr` only when an inline `on*=` handler actually
runs. The spec catches a handler that fires while a SURFACES page loads, such
as `onerror=` or `onload=`. It does not catch:

- A click-only `onclick=`. The spec never clicks, so the handler never runs.
- An `hx-on` handler. htmx 1.9.12 compiles it with `new Function` only when its
  event fires, and `hx-on::load` never fires for content that is on the page at
  load. When an hx-on handler does run, Chromium reports it as `script-src`
  with blocked URI `eval`, not as `script-src-attr`.

The reviewer tried both cases and the spec still passed.
`scripts/ci/guard-no-inline-handlers.sh` stays the primary control for both.
The header comment now states this scope. The Dev's first draft claimed hx-on
was covered.

## TDD evidence

The Dev ran this first, and the reviewer ran it again in its own worktree:

- **Red.** We temporarily added `<img src="/public/nope.png" alt=""
  onerror="void 0">` to `web/ui/pages/reports.html`. The inventory changed to
  `{"unique_violations":318,…"script-src-attr":1,…}`. The spec failed on the
  new assertion: `inline event-handler attributes violate script-src-attr: [{
  …"document_path": "/reports", "sample": "void 0" … }]`.
- **Green.** After reverting the template, the inventory had 317 unique
  entries: `script-src` 4, `script-src-elem` 99, `style-src-attr` 200,
  `style-src-elem` 14. The spec passed.
- The orchestrator re-ran the final version after the review fixes: 1 passed,
  same inventory.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The header comment and the Dev's caveat said hx-on and click-only handlers are caught at runtime. They are not. | Fixed: the comment now gives the real scope and points to the static guard. |
| 2 | Low | Could the settle loop pass too early? | Accepted. A new unique key would have to arrive more than 1s after the previous read, which is unlikely: the last navigation already waited for `networkidle`, then the first poll ran, then at least one more interval. |
| 3 | Low | Worst case could exceed the 45s test timeout, and a failed settle poll gave a bare `expected true`. | Fixed: the settle poll now has a message, and the test timeout is 60s. |
| 4 | Nit | The two poll bodies duplicate the GET /csp-report call. | Accepted. It keeps the diff small and matches the existing block. |

## Gate

- `npx playwright test --project=csp` passed.
- `guard-e2e-fixtures-import.sh`, `guard-e2e-no-browser.sh`,
  `guard-no-inline-handlers.sh` and `guard-i18n.sh` all passed.
- No Go, template, locale or help changes, so `gofmt`, build, tests and lint
  are unaffected.

## Verdict

Safe to merge. Nothing is deferred: the static guard already covers what the
runtime check cannot see.
