# Review: e2e `deactivateAllTables` fails fast (ut-docs#4458)

**Date:** 2026-10-16 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

- `e2e/tests/helpers.ts` `deactivateAllTables`: the `for (;;)` loop is gone.
  Every pass must lower the Deactivate-button count; a pass that does not
  throws `Deactivate made no progress (N still active): <err key>`, reading
  the key from the `/tables?err=` redirect. A pass that lands anywhere but
  `/tables` (e.g. a full-page 403) throws too, instead of returning with
  tables still active. The strictly falling count bounds the loop at the
  starting count.
- New `e2e/tests/deactivate-all-tables-fail-fast-4458.spec.ts`: mocks every
  Deactivate POST at the network layer (`page.route`, same hook as
  `htmx-panel-swap-error-banner-2179.spec.ts`) with a 303 to
  `/tables?err=x`. The helper must throw after exactly one refused POST,
  naming `x`. The card suggested a test-only route; a network mock proves
  the same thing without adding a test-only handler to the Go server.

## Verification

- TDD: against the pre-fix helper the new spec fails (spins ~52 s to the
  test timeout, wrong error); with the fix it passes in ~2 s.
- All 10 specs that call the helper (35 tests) pass, re-run after the
  review fixes.
- Guards from `ci.yml`'s build job: all pass locally except
  `guard-deadcode-baseline.sh` (local deadcode built with go1.26 vs the
  module's go1.27) and `guard-shellcheck-version.sh` (no shellcheck in the
  container). Both fail the same way on `main`; no Go or shell file changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | A non-redirect refusal (e.g. a 403 page) has no Deactivate buttons, so the count "drops" to 0 and the helper returns with tables still active (pre-existing). | Fixed: throw unless the pass lands on `/tables`. |
| 2 | Low (flake) | The spec's `< 10 s` wall-clock bound covered a PIN login and two navigations; redundant with `refused === 1`. | Fixed: removed. |

Reviewer checked and cleared: the count cannot fail to drop after a real
deactivate (server-rendered page; "Activate" doesn't match `hasText:
'Deactivate'`; no other `/active` forms on `/tables`); `page.url()` is the
post-redirect URL after `load`; the route is page-scoped and cleaned up.

**Verdict:** safe to merge. No user-facing change, so no manual update.
