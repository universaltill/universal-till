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

// Two tills under test:
//  - the DEFAULT project: auth off, demo catalog seeded by the migrations
//    — every spec except AUTH_ONLY_SPECS drives this one directly.
//  - the AUTH project: auth ON, a genuinely fresh install — only
//    AUTH_ONLY_SPECS drives this one, since it needs the real first-boot
//    wizard / PIN login flow the default project deliberately bypasses.
// Both boot a REAL server; Chromium drives the layer our Go tests can't
// see (htmx swaps, Alpine, the OSK, JS errors).

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
const AUTH_ONLY_SPECS = /(login|nav-rail-lock-reachable-1346)\.spec\.ts$/;

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  // One worker: specs within a project share ONE till server, and some
  // flip server-side settings (OSK mode) — parallel workers would race
  // each other's state. Kept global (not per-project) so the two
  // projects' servers are never driven concurrently either.
  workers: 1,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  webServer: [
    {
      command: 'bash ./run-till.sh',
      url: 'http://127.0.0.1:8091/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
    {
      command: 'bash ./run-till-auth.sh',
      url: 'http://127.0.0.1:8092/healthz',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
    },
  ],
  projects: [
    {
      name: 'default',
      testIgnore: AUTH_ONLY_SPECS,
      use: {
        baseURL: 'http://127.0.0.1:8091',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
    {
      name: 'auth',
      testMatch: AUTH_ONLY_SPECS,
      use: {
        baseURL: 'http://127.0.0.1:8092',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        launchOptions,
      },
    },
  ],
});
