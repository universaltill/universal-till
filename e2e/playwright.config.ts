import { defineConfig } from '@playwright/test';

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
const AUTH_ONLY_SPECS = /login\.spec\.ts$/;

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
      },
    },
    {
      name: 'auth',
      testMatch: AUTH_ONLY_SPECS,
      use: {
        baseURL: 'http://127.0.0.1:8092',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
      },
    },
  ],
});
