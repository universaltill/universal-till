import { defineConfig } from '@playwright/test';
import * as path from 'path';

// A fixed instant (RFC3339, UTC) passed to both throwaway tills as
// UT_DOCS_SHOTS_NOW so internal/clock.Now pins "now" for the run — the one
// knob that makes the receipt-designer and back-office-problems screenshots
// byte-stable (ut-docs#930). The exact value is arbitrary; it only has to be
// constant, and UTC so the rendered date can't drift with the runner's
// timezone.
const DOCS_SHOTS_NOW = '2026-01-02T15:04:05Z';

// The env both docs tills get on top of the inherited one. ut-docs#4280:
// UT_DOCS_SHOTS_DISK_ROOMY makes internal/diskspace.Probe report a fixed
// roomy disk, so the host's free space never reaches a screenshot as the
// "Storage almost full" chip. Like UT_DOCS_SHOTS_NOW, set ONLY here, never
// in run-till*.sh, so the real e2e suite keeps the live probe.
//
// UT_DOCS_SHOTS_SEED_SALES makes run-till.sh also run e2e/seed_docs_sales:
// three completed sales + two invoices at FIXED December-2025 dates
// (ut-docs#359), so the `reports` and `invoices` topics show real data. Like
// the two above, set ONLY here: the e2e suite's tills stay sale-free. The
// screenshots address that fixed window through pinnedQuery in
// tests-docs/docs-shots.spec.ts.
const DOCS_SHOTS_ENV = {
  // The server renders dates in time.Local; pin it so a regenerating machine
  // in any timezone (e.g. UTC+13) prints the same dates as CI (ut-docs#359).
  TZ: 'UTC',
  UT_DOCS_SHOTS_NOW: DOCS_SHOTS_NOW,
  UT_DOCS_SHOTS_DISK_ROOMY: '1',
  UT_DOCS_SHOTS_SEED_SALES: '1',
};

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
      // Pin "now" so any screen that legitimately renders the current time
      // captures byte-identically run-to-run (ut-docs#930): the receipt
      // designer's sample ticket date, and the back-office "recent problems"
      // panel's timestamps. Read by internal/clock.Now; set ONLY here (NOT in
      // the shared run-till.sh, which the real e2e suite also boots and which
      // must keep the live clock). Passed on top of the inherited env.
      env: DOCS_SHOTS_ENV,
    },
    {
      command: 'bash ./run-till-auth.sh',
      url: 'http://127.0.0.1:8092/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
      env: DOCS_SHOTS_ENV,
    },
  ],
  projects: [
    {
      name: 'docs',
      use: {
        baseURL: 'http://127.0.0.1:8091',
        // The product's reference kiosk viewport — every screenshot in the
        // manual is taken at the same size. Pinned explicitly alongside the
        // viewport (ut-docs#2184) rather than left to Playwright's default:
        // this harness's determinism proof (scripts/ci/guard-docs-shots-determinism.sh)
        // depends on every knob that can affect a rendered pixel being
        // pinned, not merely "probably already 1 today".
        viewport: { width: 1024, height: 600 },
        deviceScaleFactor: 1,
        trace: 'retain-on-failure',
        launchOptions: {
          // ut-docs#2184: GPU-accelerated rasterization (Chromium's default,
          // even headless — it uses SwiftShader for GL) turned out to be the
          // actual root cause of the "43 unrelated screenshots churn by
          // <20 bytes" bug, not per-locale content or timing. Two identical
          // `make docs-shots` runs on the same machine, same Chromium build,
          // produced pixel-identical output everywhere EXCEPT a handful of
          // anti-aliased edges on repeated vector icons (confirmed live: a
          // pixel-diff of two `tr/catalog` captures isolated the residual to
          // ~67 pixels, all landing at the SAME relative offset inside each
          // of 5 repeated product-tile icons, spaced exactly one tile-width
          // apart — i.e. one shared icon whose AA edge rasterizes
          // differently run-to-run, not per-tile content). That is GPU
          // compositor/raster nondeterminism (thread-scheduling-dependent
          // blend order on tile boundaries), a well-documented class of
          // Chromium screenshot flake — NOT a font/text issue like the
          // ut-docs#930 residual this comment block used to describe.
          // Forcing the software (CPU) rasterizer removes the
          // nondeterminism at its source: verified byte-identical PNGs
          // across repeated `make docs-shots` runs with these flags, where
          // the same runs without them reliably differed (scripts/ci/
          // guard-docs-shots-determinism.sh; the sale-CSS-only regeneration
          // experiment described in the bug report also stopped touching
          // `catalog`).
          //   --disable-gpu / --disable-gpu-compositing: render+composite in
          //     software instead of via the GPU process, eliminating the
          //     driver/thread-scheduling variance across runs.
          //   --disable-partial-raster, --disable-skia-runtime-opts: skip
          //     Skia's incremental-raster caching and CPU-feature-dispatched
          //     code paths — both are legitimate perf optimizations that
          //     also introduce run-to-run ordering/codepath variance for a
          //     one-shot screenshot harness that doesn't need them.
          //   --run-all-compositor-stages-before-draw: makes compositing
          //     fully synchronous, so a screenshot can never observe a
          //     partially-composited frame regardless of the animation/rAF
          //     waits already in capture().
          //   --force-color-profile=srgb: pins color management so the
          //     result can't depend on a monitor/profile-store difference
          //     between the two headless processes' environments.
          //   --default-tile-width/--default-tile-height=1024 (ut-docs#2929):
          //     one raster tile per layer covers the whole 1024x600
          //     viewport, so no shape inside the frame can straddle a tile
          //     edge (effective only on the software raster path the flags
          //     above already force). With the default 256px tiles
          //     the rail's Orders button (y 233-281) and Help button
          //     (y 468-516) are exactly the two rail items that cross the
          //     y=256 / y=512 boundaries, and they were the only pixels that
          //     flipped between runs on the CI runner (25 px, anti-aliased
          //     arcs, ~70% of run pairs; never reproduced locally). Every
          //     other rail button sits wholly inside one tile and never
          //     differed. Pinned: 10/10 dispatch runs byte-identical, vs
          //     main failing on the same runners at the same time.
          // Scoped to THIS config only — never merged into
          // playwright.config.ts's launchOptions, so the real e2e suite (and
          // any interactive/manual Chromium run) keeps normal GPU-accelerated
          // rendering. Inert in production: nothing here reaches the actual
          // till binary or a shop's browser at all — these are Chromium
          // command-line flags for the test harness's OWN browser process.
          args: [
            '--disable-gpu',
            '--disable-gpu-compositing',
            '--disable-partial-raster',
            '--disable-skia-runtime-opts',
            '--run-all-compositor-stages-before-draw',
            '--force-color-profile=srgb',
            '--default-tile-width=1024',
            '--default-tile-height=1024',
          ],
          // ut-docs#4407: the app ships no webfont, so pin which system font
          // its stack resolves to — Inter, via this harness's own fontconfig
          // (system config + Inter aliases), never the machine's /etc/fonts.
          // tests-docs/lib.js's font check fails the run if it didn't take.
          env: { ...process.env, FONTCONFIG_FILE: path.join(__dirname, 'fontconfig', 'fonts.conf') },
          // Set by scripts/docs-shots.sh only when resolve-chromium.sh found
          // a pre-installed browser worth reusing (ut-docs#622) — unset (and
          // so this key is omitted) on any machine that ran the normal
          // `playwright install --with-deps chromium` path.
          ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE
            ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE }
            : {}),
        },
      },
    },
  ],
});
