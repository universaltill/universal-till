import { test as base, expect } from '@playwright/test';
import { drainParkedOrders } from './helpers';
import { startWorkerTill } from './worker-till';

// ut-docs#1315: every spec in a WORKER shares ONE live till server
// (`Engine` is a server-side singleton — since ut-docs#2345 the `default`
// project boots one server per worker, see `workerServerURL` below, and
// the other projects still share a single static one), so a spec file
// that leaves state behind (a
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

// ut-docs#2345: worker-level options/fixtures.
//  - `e2eWorkerServer` is a project-level OPTION (set via `use:` in
//    playwright.config.ts). Only the `default` project sets it; every
//    other project leaves it at the `false` default and is untouched by
//    the fixture below — same static `baseURL` from its `use:` block,
//    same `webServer` entry, no behaviour change.
//  - `workerServerURL` boots one till per WORKER when that option is on
//    (worker-till.ts: own port, own throwaway data dir, torn down when
//    the worker ends) and resolves to `undefined` otherwise.
//  - `baseURL` (Playwright's own, test-scoped option) is then overridden
//    to prefer the worker's server. It must stay TEST-scoped: Playwright
//    refuses to re-register a fixture at a different scope (load error
//    "has already been registered as a { scope: 'test' } fixture"), which
//    is why the worker server lives in its own fixture and `baseURL` only
//    forwards it. `page`, `context` and `request` all derive their base
//    URL from this option, so a `page.goto('/')` and the reset POST
//    below both land on the worker's own server.
type WorkerOpts = { e2eWorkerServer: boolean; workerServerURL: string | undefined };

export const test = base.extend<{ resetPosOncePerFile: void }, WorkerOpts>({
  e2eWorkerServer: [false, { scope: 'worker', option: true }],
  workerServerURL: [
    async ({ e2eWorkerServer }, use, workerInfo) => {
      if (!e2eWorkerServer) {
        await use(undefined);
        return;
      }
      const till = await startWorkerTill(workerInfo.parallelIndex);
      try {
        await use(till.url);
      } finally {
        await till.stop();
      }
    },
    // A worker-scoped fixture with no explicit timeout inherits
    // `workerFixtureTimeout`, which Playwright sets from `project.timeout`
    // (30_000 here) — well under `worker-till.ts`'s own BOOT_TIMEOUT_MS
    // (60s), so the fixture itself would kill a slow-but-healthy boot
    // (cold-cache seeds, or the binary fallback build) before that budget
    // is ever reached. Match run-till.sh's old `webServer` entry, which
    // carried its own explicit `timeout: 120_000`.
    { scope: 'worker', timeout: 120_000 },
  ],
  baseURL: async ({ baseURL, workerServerURL }, use) => {
    await use(workerServerURL ?? baseURL);
  },
  // ut-docs#2223: every page runs as a reduced-motion user. The product's
  // cross-document View Transitions are switched off under
  // `prefers-reduced-motion: reduce` (CSS in base.html/app.css AND the
  // pageswap/pagereveal skip in base.html's script), and they must be off
  // here: in headless Chromium a link-click or POST->303 navigation starts
  // the transition but the new document is never revealed -- no
  // `pagereveal`, no paint, every hit-tested action on it hangs until the
  // test timeout (categories-record-dialog-2010, bugreport-panel, the full
  // CI suite going from ~10 min to a runner-limit hang on the first push,
  // run 35138264312). The motion itself is verified on the real devices.
  // Done here, not in playwright.config.ts: Playwright 1.61 silently drops
  // `reducedMotion` from `use`/`test.use` (verified -- `colorScheme` in the
  // same call applies, `reducedMotion` does not), while
  // `page.emulateMedia` works. A spec that needs the in-page ease opts back
  // in with `page.emulateMedia({ reducedMotion: 'no-preference' })`
  // (page-transitions-2223 does, for the swap-ease cases only).
  page: async ({ page }, use) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await use(page);
  },
  // Auto fixture — every test opts in with no changes to the test body.
  // Resets the shared till's basket ONCE per spec FILE, before that
  // file's first TEST BODY, not before every individual test: a file's
  // own tests are free to build on each other's basket state exactly as
  // before (e.g. tender-panel-reachable.spec.ts holding several sales in
  // a row within one test). Only what a DIFFERENT file left behind gets
  // cleared. Safe with parallel workers (ut-docs#2345): each worker is
  // its own Node process with its own copy of this module-level Set AND
  // (on the `default` project) its own till server, and Playwright never
  // splits one spec file across two workers — so within any one server
  // every file is still seen exactly once, sequentially.
  //
  // Ordering caveat (found in review, ut-docs#1315): this is a test-scoped
  // auto fixture, and Playwright runs `test.beforeAll` BEFORE test-scoped
  // fixtures — so the reset fires after any `test.beforeAll` a spec might
  // add, not before it. No default-project spec uses `beforeAll` today
  // (verified: only the exempt login.spec.ts does), so there's no live
  // bug, but don't seed basket OR held-order state meant to survive the
  // whole file in a `beforeAll` (ut-docs#2141 widened what this reset
  // clears — see below) — either would be silently wiped before the first
  // test runs.
  //
  // Also DRAINS held (parked) rows, once per file (ut-docs#2141) — `POST
  // /api/pos/reset` only clears the in-memory basket, never a held row
  // (that's a DB row, deleted only by resuming it — see helpers.ts's own
  // drainParkedOrders doc comment), so without this a spec file that holds
  // a sale and never resumes it (found live, and checked one by one rather
  // than assumed from a grep for "Hold Sale" — see docs/code-reviews/ for
  // this card's own review record: 5 real spots, plus
  // parked-orders-popup-2137.spec.ts's own two deliberately-refused/
  // never-resumed tests -- hold-named-tab.spec.ts,
  // new-sale-closes-payment-overlay-1386.spec.ts,
  // payment-overlay-duplicate-labels-1625.spec.ts,
  // payment-overlay-footer-reachable-1542.spec.ts and
  // tender-panel-reachable.spec.ts) leaves that row for every later file to
  // inherit. Deliberately once per FILE, same granularity as the basket
  // reset above, not once per TEST — draining is a few extra HTTP round
  // trips per round and this suite already has enough files that a
  // per-test cost would add up for no benefit: nothing here needs a drain
  // BETWEEN two tests in the same file, only between one file and the
  // next. A file whose own tests need a drained state mid-file (not just
  // at the file's first test) still calls drainParkedOrders directly, same
  // as parked-orders-popup-2137.spec.ts's own afterEach does.
  //
  // Only a `default`-project spec is actually protected by the drain
  // above: the top-level `request` fixture here carries no session cookie,
  // so on the `auth` project (login.spec.ts and its two siblings) this is
  // the same no-op the plain basket reset always was there too — not a
  // regression, just worth knowing this doesn't magically cover every
  // project.
  //
  // `resetDoneForFile.add` only happens AFTER a successful drain (not
  // before, the way a check-then-set might read at a glance) — if
  // `drainParkedOrders` throws (a row that cannot be resumed, or the
  // parked-orders listing itself erroring — both loud, deliberate
  // failures), this file is not falsely marked as already handled.
  resetPosOncePerFile: [
    async ({ request }, use, testInfo) => {
      if (!resetDoneForFile.has(testInfo.file)) {
        await drainParkedOrders(request);
        resetDoneForFile.add(testInfo.file);
      }
      await use();
    },
    { auto: true },
  ],
});

export { expect };
