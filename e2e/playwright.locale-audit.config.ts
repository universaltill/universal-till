import { defineConfig } from '@playwright/test';
import { existsSync } from 'fs';

// Sandboxed pipeline runners pre-install one Chromium at a fixed path
// (PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1, no per-version browser cache) —
// same PREINSTALLED_CHROMIUM pattern as playwright.config.ts. Everywhere
// else (CI, dev machines) this is a no-op and the normal per-version cache
// resolution applies.
const PREINSTALLED_CHROMIUM = '/opt/pw-browsers/chromium';
const launchOptions = existsSync(PREINSTALLED_CHROMIUM) ? { executablePath: PREINSTALLED_CHROMIUM } : {};

// scripts/ci/audit-locale-render.sh (ut-docs#2300): renders every help-topic
// page route in German and fails on visible English text outside
// tests-i18n-audit/allowlist.json — see
// tests-i18n-audit/audit-locale-render.spec.ts for the actual check.
//
// Deliberately a SEPARATE config, not a third project in
// playwright.docs.config.ts or playwright.config.ts: `de` is not a
// core-shipped locale (only en/ar/fa/tr ship in web/locales/) — German
// lives in the external ut-plugin-language-de repo, and exercising it here
// needs BOTH webServers below booted with UT_TEST_I18N_OVERLAY_DIR pointed
// at that pack's locales/ directory (loadTestI18nOverlays,
// internal/pages/init.go), so the till's I18n has a de.json overlay to
// render at all. That env var must never leak into playwright.docs.config.ts's
// servers — a documentation screenshot must never be captured in a
// plugin-overlaid locale nothing asked for. testDir is also its own
// directory (./tests-i18n-audit, not ./tests-docs) for the same isolation
// reason: playwright.docs.config.ts's testDir: './tests-docs' must never
// accidentally pick up this spec and break `make docs-shots`.
//
// The actual overlay directory is controlled entirely by whoever invokes
// `npx playwright test --config=playwright.locale-audit.config.ts` (see
// scripts/ci/audit-locale-render.sh) via the UT_TEST_I18N_OVERLAY_DIR env
// var already in process.env — nothing is hardcoded here.
export default defineConfig({
  testDir: './tests-i18n-audit',
  timeout: 60_000,
  retries: 0,
  // One worker: both webServers below are shared, throwaway tills, same
  // reasoning as playwright.docs.config.ts.
  workers: 1,
  reporter: 'list',
  webServer: [
    // Same throwaway tills as the docs-shots harness: the default (auth-off)
    // till serves every topic but one — GET /users needs a real manager
    // session (403 with no operator in context on UT_AUTH=off), so it is
    // audited against the auth till after the same wizard/PIN-login flow
    // docs-shots.spec.ts's own ensureOperator() drives.
    {
      command: 'bash ./run-till.sh',
      url: 'http://127.0.0.1:8091/healthz',
      timeout: 120_000,
      // Always fresh, same reasoning as playwright.docs.config.ts: a
      // reused local server could carry state (installed plugins, edited
      // settings) that changes what actually renders.
      reuseExistingServer: false,
      env: {
        ...(process.env.UT_TEST_I18N_OVERLAY_DIR
          ? { UT_TEST_I18N_OVERLAY_DIR: process.env.UT_TEST_I18N_OVERLAY_DIR }
          : {}),
      },
    },
    {
      command: 'bash ./run-till-auth.sh',
      url: 'http://127.0.0.1:8092/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
      env: {
        ...(process.env.UT_TEST_I18N_OVERLAY_DIR
          ? { UT_TEST_I18N_OVERLAY_DIR: process.env.UT_TEST_I18N_OVERLAY_DIR }
          : {}),
      },
    },
  ],
  projects: [
    {
      name: 'locale-audit',
      use: {
        baseURL: 'http://127.0.0.1:8091',
        trace: 'retain-on-failure',
        launchOptions,
      },
    },
  ],
});
