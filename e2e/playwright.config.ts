import { defineConfig } from '@playwright/test';
import { existsSync } from 'fs';

// Sandboxed pipeline runners pre-install one Chromium at a fixed path
// (PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1, no per-version browser cache), so
// the version-suffixed executable `npx playwright install` would resolve
// doesn't exist there. Point at the pre-installed binary when present;
// everywhere else (CI, dev machines) this is a no-op and the normal
// per-version cache resolution applies.
const PREINSTALLED_CHROMIUM = '/opt/pw-browsers/chromium';
const launchOptions = existsSync(PREINSTALLED_CHROMIUM) ? { executablePath: PREINSTALLED_CHROMIUM } : {};

// Three tills under test:
//  - the DEFAULT project: auth off, demo catalog seeded by the migrations
//    — every spec except AUTH_ONLY_SPECS/AI_IDENTIFY_ONLY_SPECS drives this
//    one directly.
//  - the AUTH project: auth ON, a genuinely fresh install — only
//    AUTH_ONLY_SPECS drives this one, since it needs the real first-boot
//    wizard / PIN login flow the default project deliberately bypasses.
//  - the AI-IDENTIFY project: `UT_AI_ENDPOINT` set so `.aiIdentify`
//    resolves true server-side — only AI_IDENTIFY_ONLY_SPECS drives this
//    one. Unlike barcode-scan, the ai.identify button/overlay markup
//    doesn't exist in the DOM at all when the feature is off
//    (`{{ if .aiIdentify }}` in web/ui/pages/index.html), so it can't join
//    the shared default-project till the way barcode-scan's tests do
//    (ut-docs#1559).
// All three boot a REAL server; Chromium drives the layer our Go tests
// can't see (htmx swaps, Alpine, the OSK, JS errors).

// Specs that need a real manager session the default (auth-off) till can
// never provide: login.spec.ts (the wizard/PIN flow itself). Prior to
// ut-docs#901/#902, this also had to include tables-keyboard-reposition-826
// .spec.ts and any spec driving country-settings/kitchen-stations/
// promotions/translations, because those pages' requireManager gates had no
// UT_AUTH=off bypass (the same gap tests-docs/docs-shots.spec.ts's own
// AUTH_TILL_TOPICS works around for the screenshot harness) — #901 fixed
// locations/registers, #902 fixed the remaining five, so every admin page
// is reachable on the default project now.
//
// nav-rail-lock-reachable-1346.spec.ts (ut-docs#1346) is a different class
// of gap #901/#902 didn't touch: GET /settings itself is reachable on the
// default project, but its `#session-chip` fragment (web/ui/partials/
// session_chip.html — the 3 manager admin links + operator name + Lock
// button this spec measures) is rendered from `auth.FromContext(r.Context())`
// (auth_page.go's `GET /ui/session-chip`), which is only ever populated by
// `auth.Middleware` resolving a real session cookie — a middleware that is
// never installed at all when UT_AUTH=off (internal/pages/init.go), so the
// chip renders empty on the default project regardless of canPerform()'s
// bypass. Confirmed live: the default project's `.session-admin-link`
// count is 0, not 3. Needs the auth project's real PIN-login session, same
// as login.spec.ts — this spec runs AFTER login.spec.ts in file-sort order
// (verified: `playwright test --project=auth --list`), so it always finds
// the wizard-created admin operator already in place and never races
// login.spec.ts's own "brand-new till" first assertion.
const AUTH_ONLY_SPECS =
  /(login|nav-rail-lock-reachable-1346|nav-rail-svg-icons-lock-1423|session-expiry-redirect-2144|session-expiry-redirect-admin-2157)\.spec\.ts$/;

// ut-docs#1559: the ai.identify overlay's own err.name branching coverage
// needs the dedicated ai-identify project/server below — see the comment
// on the webServer/projects entries for why it can't share the default
// project's till.
const AI_IDENTIFY_ONLY_SPECS = /camera-error-branching-ai-identify-1559\.spec\.ts$/;

// ut-docs#1904 / ADR-0088: the layout-plugin spec drives a till with the
// real plugins/layout-salon installed, which HIDES /tables and
// /kitchen-stations and re-labels /items. Installing that into the shared
// default till would move the ground under every other menu/nav assertion
// in the suite, so it gets its own server + project.
const LAYOUT_ONLY_SPECS = /layout-plugin-menu-1904\.spec\.ts$/;

// ut-docs#2169 / ADR-0092: the diagnostic-mode spec REGISTERS its till
// against an in-process fake ut-cloud (run-till-diagnostics.sh points
// UT_MARKETPLACE_ENDPOINT_URL at the port the spec listens on). Doing that
// to the shared default till would flip its Registration card — and what
// every cloudsync tick does — for every later spec in the run, so it gets
// its own server + project, same reasoning as the layout project above.
const DIAGNOSTICS_ONLY_SPECS = /diagnostic-mode-indicator-2169\.spec\.ts$/;

// ut-docs#2345: the `default` project's till is NOT in the `webServer`
// list below. Its ~134 spec files run in parallel, and `internal/pos.Engine`
// is a server-side singleton, so one shared server would let workers race
// each other's basket/settings state — instead every worker boots its OWN
// server (tests/worker-till.ts, via the `workerServerURL` fixture in
// tests/fixtures.ts) on port 9091 + parallelIndex, with its own throwaway
// data dir, torn down when the worker ends. `e2eWorkerServer: true` in the
// project's `use:` is what switches that on. The other four projects keep
// a single static server each (they have 1-2 spec files, nothing to
// parallelise) and are deliberately untouched by this.
type WorkerOptions = { e2eWorkerServer: boolean };

// ut-docs#2345: per-project worker cap for every project that still drives
// ONE static server. Found live on the first 4-worker run: the `auth`
// project has FIVE spec files, and with the global cap alone Playwright
// spread them over several workers against the same 8092 till — a
// nav-rail spec completed the first-boot wizard while login.spec.ts was
// still expecting a never-set-up install (`/setup` answered 303 → /login),
// which is exactly the cross-file ordering the file-sort comment above
// AUTH_ONLY_SPECS relies on. `workers: 1` on the project keeps those files
// sequential in one worker, as the whole suite used to be. The three
// single-file projects can't be split today, but carrying the same cap
// means adding a second file to one of them stays safe by construction.
const STATIC_SERVER_WORKERS = 1;

export default defineConfig<{}, WorkerOptions>({
  testDir: './tests',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  // Builds the till binary once per run for the per-worker servers.
  globalSetup: require.resolve('./global-setup'),
  // Parallel since ut-docs#2345: each `default`-project worker drives its
  // own till (see the comment above), so cross-worker state races are
  // gone; within a worker, files still run sequentially against that one
  // server, exactly as the whole suite did at `workers: 1`. The other four
  // projects still have ONE static server each, so each of them carries
  // its own per-project `workers: 1` below — see STATIC_SERVER_WORKERS.
  workers: process.env.CI ? 4 : 2,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  // ut-docs#2223: the suite runs as a reduced-motion user -- see the `page`
  // fixture in tests/fixtures.ts for why, and why it is NOT a
  // `use: { reducedMotion }` here (Playwright 1.61 silently drops that
  // option from `use`/`test.use`; `page.emulateMedia` works).
  webServer: [
    {
      command: 'bash ./run-till-auth.sh',
      url: 'http://127.0.0.1:8092/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
    {
      command: 'bash ./run-till-ai.sh',
      url: 'http://127.0.0.1:8093/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
    {
      command: 'bash ./run-till-layout.sh',
      url: 'http://127.0.0.1:8094/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
    {
      command: 'bash ./run-till-diagnostics.sh',
      url: 'http://127.0.0.1:8095/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
  ],
  projects: [
    {
      name: 'default',
      testIgnore: [AUTH_ONLY_SPECS, AI_IDENTIFY_ONLY_SPECS, LAYOUT_ONLY_SPECS, DIAGNOSTICS_ONLY_SPECS],
      use: {
        // No static baseURL: the `workerServerURL` fixture supplies this
        // worker's own server (9091 + parallelIndex) — see the note above
        // the `workers` setting.
        e2eWorkerServer: true,
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
    {
      name: 'auth',
      testMatch: AUTH_ONLY_SPECS,
      workers: STATIC_SERVER_WORKERS,
      use: {
        baseURL: 'http://127.0.0.1:8092',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
    {
      name: 'ai-identify',
      testMatch: AI_IDENTIFY_ONLY_SPECS,
      workers: STATIC_SERVER_WORKERS,
      use: {
        baseURL: 'http://127.0.0.1:8093',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
    {
      name: 'layout',
      testMatch: LAYOUT_ONLY_SPECS,
      workers: STATIC_SERVER_WORKERS,
      use: {
        baseURL: 'http://127.0.0.1:8094',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
    {
      name: 'diagnostics',
      testMatch: DIAGNOSTICS_ONLY_SPECS,
      workers: STATIC_SERVER_WORKERS,
      use: {
        baseURL: 'http://127.0.0.1:8095',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
  ],
});
