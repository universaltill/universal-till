import { defineConfig } from '@playwright/test';
import base, { EXCLUSIVE_SPECS, type WorkerOptions } from './playwright.config';

// WebKit phone lane (ut-docs#3734). The iOS app (WKWebView) and the Pi shell
// (WebKitGTK) render in WebKit, which Chromium-only specs never exercise.
// This config runs the specs whose PURPOSE is phone width under Playwright
// WebKit, against per-worker tills exactly like the `default` project.
//
// A separate config, not a project in playwright.config.ts: `webServer` is
// top level, so a project there would still boot all the static tills, and a
// plain `npx playwright test` would need WebKit installed. Run it with
//   npx playwright install webkit && npx playwright test -c playwright.webkit.config.ts
// CI: the `webkit-phone` job in .github/workflows/e2e.yml.

// "phone" in the file NAME (not the path) is the rule for joining the lane.
export const WEBKIT_PHONE_SPECS = /[^/\\]*phone[^/\\]*\.spec\.ts$/;

export default defineConfig<{}, WorkerOptions>({
  ...base, // timeout, expect, retries, workers, globalSetup, testDir
  webServer: [], // per-worker tills only; no static servers
  outputDir: 'test-results-webkit',
  reporter: process.env.CI
    ? [['list'], ['html', { open: 'never', outputFolder: 'playwright-report-webkit' }]]
    : 'list',
  projects: [
    {
      name: 'webkit-phone',
      testMatch: WEBKIT_PHONE_SPECS,
      // Drops the auth-project phone-drawer-* specs (they need the static auth till).
      testIgnore: EXCLUSIVE_SPECS,
      use: {
        browserName: 'webkit',
        e2eWorkerServer: true,
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        // No launchOptions: the preinstalled Chromium executablePath must
        // never reach WebKit.
      },
    },
  ],
});
