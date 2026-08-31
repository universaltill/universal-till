import { test as base, expect } from '@playwright/test';

// ut-docs#1315: every spec in a project shares ONE live till server
// (`Engine` is a server-side singleton — see playwright.config.ts's
// `workers: 1` comment), so a spec file that leaves state behind (a
// non-empty basket, a discount, an assigned table) can make an unrelated
// LATER file's exact-total/exact-copy assertions fail for reasons that
// have nothing to do with what that file is actually testing.
// ut-docs#1310 was one confirmed instance of this (settings-osk.spec.ts's
// cancelled hold-sale dialog leaking a basket item into
// split-tender-i18n-925.spec.ts's fa/RTL test) fixed by hand with a
// beforeEach reset in the one file that got bitten; this fixture is the
// systemic backstop so the NEXT file doesn't have to rediscover the same
// bug: every spec starts from a known-clean basket without having to
// remember to ask for it.
//
// Every e2e/tests/*.spec.ts (except login.spec.ts — see below) should
// import `test`/`expect` from here instead of directly from
// '@playwright/test'; scripts/ci/guard-e2e-fixtures-import.sh enforces
// this so a new spec can't silently opt back out.
//
// login.spec.ts is deliberately EXEMPT: it drives the separate `auth`
// project against a genuinely fresh, never-set-up till (`/` -> `/setup`)
// and its own test.describe.serial block depends on that exact fresh
// state — resetting `/api/pos/reset` against a pre-wizard server is
// meaningless (there's no basket to reset yet) and risks masking a
// regression in the fresh-install path itself. It also can't leak into
// or be leaked into by any `default`-project spec: the two projects run
// against two separate server processes (playwright.config.ts) and never
// share state to begin with.
const resetDoneForFile = new Set<string>();

export const test = base.extend<{ resetPosOncePerFile: void }>({
  // Auto fixture — every test opts in with no changes to the test body.
  // Resets the shared till's basket ONCE per spec FILE, before that
  // file's first TEST BODY, not before every individual test: a file's
  // own tests are free to build on each other's basket state exactly as
  // before (e.g. tender-panel-reachable.spec.ts holding several sales in
  // a row within one test). Only what a DIFFERENT file left behind gets
  // cleared. Safe because `playwright.config.ts` pins `workers: 1` for
  // this project — every file runs sequentially in the same worker
  // process, so this module-level Set sees every file exactly once
  // across the whole run.
  //
  // Ordering caveat (found in review, ut-docs#1315): this is a test-scoped
  // auto fixture, and Playwright runs `test.beforeAll` BEFORE test-scoped
  // fixtures — so the reset fires after any `test.beforeAll` a spec might
  // add, not before it. No default-project spec uses `beforeAll` today
  // (verified: only the exempt login.spec.ts does), so there's no live
  // bug, but don't seed basket state meant to survive the whole file in a
  // `beforeAll` — it would be silently wiped by this reset before the
  // first test runs.
  resetPosOncePerFile: [
    async ({ request }, use, testInfo) => {
      if (!resetDoneForFile.has(testInfo.file)) {
        resetDoneForFile.add(testInfo.file);
        await request.post('/api/pos/reset');
      }
      await use();
    },
    { auto: true },
  ],
});

export { expect };
