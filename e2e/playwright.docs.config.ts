import { defineConfig } from '@playwright/test';

// The manual's screenshot harness (`make docs-shots`) — captures every routed
// help topic at 1024×600 in every shipped locale into web/help/img/.
//
// Deliberately a SEPARATE config file, not a third project in
// playwright.config.ts: .github/workflows/e2e.yml runs a plain
// `npx playwright test`, which picks up playwright.config.ts by filename and
// runs EVERY project in it when none is named — so the docs harness gets its
// own config (and its own testDir) to stay off the normal CI run entirely.
// It only ever runs via `make docs-shots`.
export default defineConfig({
  testDir: './tests-docs',
  timeout: 60_000,
  retries: 0,
  // One worker, same reasoning as playwright.config.ts: all specs share ONE
  // till server, and the sell screenshots put lines in its (server-side)
  // basket.
  workers: 1,
  reporter: 'list',
  webServer: [
    // Same throwaway tills as the e2e suite. The default (auth-off) till
    // serves every topic but one: /users requires a real manager session
    // (403 with no operator in context), so the users topic is captured on
    // the auth till after the same wizard/PIN flow login.spec.ts drives.
    {
      command: 'bash ./run-till.sh',
      url: 'http://127.0.0.1:8091/healthz',
      timeout: 120_000,
      // Unlike playwright.config.ts's own servers, this one is ALWAYS fresh
      // — reused here on purpose would mean whatever a developer's prior
      // local run left behind (completed sales, flipped settings, an
      // installed plugin) silently bakes into "reproducible" documentation
      // screenshots. make docs-shots is a deliberate, occasional run, not
      // the tight edit/test loop reuseExistingServer optimizes for.
      reuseExistingServer: false,
    },
    {
      command: 'bash ./run-till-auth.sh',
      url: 'http://127.0.0.1:8092/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
    },
  ],
  projects: [
    {
      name: 'docs',
      use: {
        baseURL: 'http://127.0.0.1:8091',
        // The product's reference kiosk viewport — every screenshot in the
        // manual is taken at the same size.
        viewport: { width: 1024, height: 600 },
        trace: 'retain-on-failure',
        // Set by scripts/docs-shots.sh only when resolve-chromium.sh found a
        // pre-installed browser worth reusing (ut-docs#622) — unset (and so
        // this is a no-op) on any machine that ran the normal
        // `playwright install --with-deps chromium` path.
        ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE
          ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE } }
          : {}),
      },
    },
  ],
});
