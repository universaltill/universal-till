import { defineConfig } from '@playwright/test';

// The till under test: webServer builds+boots a REAL server on a throwaway
// data dir (auth off, demo catalog seeded by the migrations). Every spec
// drives a real Chromium against it — the layer our Go tests can't see
// (htmx swaps, Alpine, the OSK, JS errors).
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  // One worker: specs share ONE till server, and some flip server-side
  // settings (OSK mode) — parallel workers would race each other's state.
  workers: 1,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: 'http://127.0.0.1:8091',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'bash ./run-till.sh',
    url: 'http://127.0.0.1:8091/healthz',
    timeout: 120_000,
    reuseExistingServer: !process.env.CI,
  },
});
