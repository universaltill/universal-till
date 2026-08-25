import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// The manual's screenshot harness (`make docs-shots`, ut-docs#327): for every
// help topic that declares a routes: front-matter field, capture its FIRST
// route at the reference kiosk viewport (1024×600, set in
// playwright.docs.config.ts) in every shipped locale, into
// web/help/img/<locale>/<id>.png — the repo checkout, at build time; these
// are committed assets, embedded into the binary by web/embed.go's
// `//go:embed help`. Route-less topics are skipped on purpose (follow-up
// card). Screens are captured against the same throwaway till servers as the
// e2e suite: fresh DB, demo catalog seeded deterministically by the
// migrations.
const { repoRoot, LOCALES, routedTopics } = require('./lib');

const imgRoot = path.join(repoRoot, 'web', 'help', 'img');

// The users topic's first route (/users) requires a real manager session —
// with UT_AUTH=off there is no operator in the request context, so the
// auth-off till 403s it. It is the one topic captured against the AUTH
// server (8092) after the same wizard/PIN flow login.spec.ts drives; every
// other topic uses the default auth-off till (8091, the config's baseURL).
const AUTH_BASE = 'http://127.0.0.1:8092';
const ADMIN_PIN = '482913'; // same PIN login.spec.ts sets, so a reused local auth server still logs in

// Extra query params pinned per topic so no screenshot depends on when it was
// taken. /reports' widgets look back N days from "now" — the till's DB is
// fresh (no sales either way), but pin the lookback so the rendered filter
// state can never drift with the calendar.
const pinnedQuery: Record<string, string> = {
  reports: 'days=30',
};

// CLOCK-PINNED FOR DETERMINISM (ut-docs#930, was the accepted gap ut-docs#327
// flagged): two screens legitimately render the current time server-side —
// the `designer` topic's receipt preview (#rd-preview, /receipt-designer:
// sampleReceiptDoc's Meta line) and the `alerts` topic's back-office "recent
// problems" panel (/backoffice: each Problem's `.At` timestamp). Both now go
// through internal/clock.Now, which this harness pins to a fixed instant via
// UT_DOCS_SHOTS_NOW (set on the webServers in playwright.docs.config.ts). So
// their dates are byte-stable here while staying the real wall clock in
// production — no masking needed, and the topic's actual content is kept in
// frame. This removes what ut-docs#930 observed as the CONSISTENT,
// every-single-run churn (the 8 alerts/designer PNGs), which was pure
// timestamp drift.
//
// A SMALLER, INTERMITTENT residual remains and is NOT chased to zero here: on
// the heaviest text screens (the ~hundreds-of-rows ar/translations table,
// occasionally invoices) a handful of anti-aliased pixels on a single glyph
// can still toggle between two rasterizations run-to-run — measured at ~10
// pixels, a sub-10-byte PNG delta. That is browser text-rasterization
// nondeterminism, the same reason Playwright's own toHaveScreenshot compares
// with a pixel TOLERANCE rather than byte-equality; it survives every
// DOM-side settle below (htmx-idle wait, fonts.ready, rAF, animations
// disabled). It is deliberately left as-is rather than over-engineered around
// (ut-docs#930 close-out notes a follow-up if pixel-exactness is ever needed).
//
// THE MANIFEST-vs-PNG CONTRACT (ut-docs#930 AC): guard-docs-shots.sh checks
// freshness from SOURCE-surface hashes recorded in manifest.json, and never
// hashes the PNG bytes — precisely so this AA noise cannot fail CI. So a PR
// that touches a screened surface must regenerate and commit manifest.json
// (its recorded surface hash moves), but need only commit the PNGs whose
// CONTENT actually changed; regenerated PNGs that differ only by AA noise
// should be reverted, not committed. A manifest-only (or manifest-plus-the-
// -real-PNGs) commit is therefore a LEGITIMATE, intended outcome — the
// ut-docs#925 workaround was correct, just previously undocumented. The clock
// pin above means this manual triage is now rare and tiny, not the 8-file
// every-run event ut-docs#930 reported.

// A blank white 1024×600 PNG compresses to ~2 KB — anything at or below that
// is a broken capture (error page, unstyled shell), and must fail the run
// rather than ship as "documentation".
const MIN_BYTES = 4096;

async function capture(page: Page, id: string, locale: string, route: string) {
  const sep = route.includes('?') ? '&' : '?';
  let url = `${route}${sep}lang=${locale}`;
  if (pinnedQuery[id]) url += `&${pinnedQuery[id]}`;
  await page.goto(url, { waitUntil: 'networkidle' });

  // The locale actually took: RTL locales must render flipped, same
  // assertion as rtl.spec.ts — a screenshot of the wrong locale (or of a
  // redirect target that dropped ?lang) is worse than no screenshot.
  const dir = ['fa', 'ar'].includes(locale) ? 'rtl' : 'ltr';
  await expect(page.locator('html')).toHaveAttribute('dir', dir);

  // Wait for any htmx request/swap/settle to be fully done before capturing.
  // Several topics lazy-load their body after first paint (e.g.
  // /translations' key table is an hx-trigger="load" fragment), and htmx runs
  // a brief settle phase — marked by the .htmx-request/.htmx-swapping/
  // .htmx-settling classes — during which the DOM is mid-transition. goto's
  // networkidle covers the fetch but not the settle, so capturing here raced
  // the swap: the heaviest such table (ar/translations, hundreds of
  // Arabic-script rows) came out with a different byte hash on nearly every
  // run (ut-docs#930). Poll until no htmx-in-flight marker remains.
  await page
    .waitForFunction(
      () => !document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling'),
      null,
      { timeout: 10_000 },
    )
    .catch(() => {}); // no htmx on the page at all → nothing to wait for

  // Let webfonts finish before capturing, or Arabic-script shots race the
  // font swap.
  await page.evaluate(() => (document as any).fonts.ready);

  // fonts.ready resolves when the font FILES have loaded, not when the
  // browser has actually repainted the glyphs with them — a one-frame gap
  // that intermittently captured a page mid-swap (ut-docs#930: `invoices`
  // churning in a single locale on ~half of runs, with no source change).
  // Wait two animation frames so a real paint has landed before the shot.
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );

  const out = path.join(imgRoot, locale, `${id}.png`);
  fs.mkdirSync(path.dirname(out), { recursive: true });
  // Masked, both for the same reason: driven by real machine/runtime state
  // rather than the seeded till itself, so leaving them unmasked would make
  // otherwise-identical captures differ between machines/runs.
  //  - .sb-update: the update-available chip, only appears when the
  //    background update check actually reaches the marketplace.
  //  - .sb-conn: online/offline, driven client-side by navigator.onLine —
  //    genuinely differs on a network-isolated capture box.
  // maskColor = the statusbar's own background (web/public app.css
  // .statusbar), so a mask reads as empty bar, not a magenta box. That color
  // is itself part of the guarded "surface" fileset now, so a theme change
  // that moves it fails the freshness guard instead of silently mismatching.
  await page.screenshot({
    path: out,
    // Finish/disable CSS transitions & animations so a mid-flight htmx fade or
    // hover transition can't bake a half-painted frame into the shot — the
    // other half of the ut-docs#930 stabilization, alongside the htmx-idle
    // wait above.
    animations: 'disabled',
    mask: [page.locator('.sb-update'), page.locator('.sb-conn')],
    maskColor: '#0f172a',
  }); // viewport-sized, per config

  // A broken/blank capture must fail loudly, not ship silently.
  expect(fs.existsSync(out), `${out} was not written`).toBe(true);
  expect(fs.statSync(out).size, `${out} is suspiciously small — blank capture?`).toBeGreaterThan(MIN_BYTES);
}

// Same barcodes as sale.spec.ts (demo catalog, checksums fixed by migration
// 023): the sell screenshots should show a shop mid-sale, not an empty till.
async function ensureBasketLines(page: Page) {
  await page.goto('/');
  const basket = page.locator('#basket');
  if ((await basket.textContent())?.includes('Coca-Cola')) return; // already staged
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(basket).toContainText('Coca-Cola');
  await page.getByRole('textbox').first().fill('5000000000029');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(basket).toContainText('Pepsi');
}

// GET /invoices redirects to /settings until a seller identity is configured
// (that's what turns the feature on) — set it through the till's own settings
// API, which UT_AUTH=off permits (isManagerOrAuthOff), so the invoice
// register actually renders. Idempotent, so it just re-runs per shot.
async function ensureInvoiceSeller(page: Page) {
  const res = await page.request.post('/api/settings/invoice', {
    form: {
      seller_name: 'Demo Shop Ltd',
      seller_address: '1 High Street, Demo Town',
      seller_vat_no: 'GB123456789',
    },
  });
  expect(res.status(), 'configuring the invoice seller failed').toBe(204);
}

// The auth till (8092) is a genuinely fresh install: complete the first-boot
// wizard if it appears (fresh server), or PIN-login (server reused from a
// local e2e run that already set it up) — mirrors login.spec.ts.
async function ensureOperator(page: Page) {
  await page.goto('/');
  if (page.url().includes('/setup')) {
    // ut-docs#617 inserted a new step 5 whose default panel has no "Next"
    // button at all (No / Yes / Later instead) — the old flat click
    // sequence of bare `.setup-nav button:visible` presses would have
    // hunted for a "Next" that isn't there at that point. Scoped to each
    // numbered section (data-step, set on every <section> in setup.html)
    // rather than trying to keep the flat sequence in sync by count.
    const step = (n: number) => page.locator(`[data-step="${n}"]`);
    await step(1).locator('.setup-nav button', { hasText: 'Next' }).click(); // language
    await page.locator('select[name=country]').selectOption('GB');
    await step(2).locator('.setup-nav button', { hasText: 'Next' }).click(); // country
    await page.locator('input[name=store_name]').fill('Demo Shop');
    await step(4).locator('.setup-nav button', { hasText: 'Next' }).click(); // shop name (step 3 is the DE-only business-identity step — GB skips it)
    await step(5).locator('.setup-nav button', { hasText: 'Next' }).click(); // shop type + demo data (ut-docs#539, both optional)
    await step(6).locator('.setup-nav button.primary', { hasText: 'No' }).click(); // restore from another POS? (ut-docs#617) — No, starting fresh
    await step(7).locator('input[name=pin]').fill(ADMIN_PIN);
    await step(7).locator('input[name=pin_confirm]').fill(ADMIN_PIN);
    await step(7).locator('.setup-nav button', { hasText: 'Next' }).click(); // PIN
    await Promise.all([
      page.waitForURL((u) => !u.pathname.includes('/setup')),
      step(8).locator('button[type=submit]', { hasText: 'Start selling' }).click(),
    ]);
  } else if (page.url().includes('/login')) {
    for (const d of ADMIN_PIN.split('')) {
      await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
    }
    await page.locator('button[type=submit].pin-key').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));
  }
  await expect(page.locator('#basket')).toBeVisible();
}

// Topics whose first route requires a real manager session — UT_AUTH=off has
// no operator in the request context, so the default till 403s them. Each is
// captured on the AUTH server (8092) instead, after the same wizard/PIN flow
// login.spec.ts drives. Started as just "users" (ut-docs#327); "translations"
// joined it here (ut-docs#326) — GET /translations has the same
// requireManager gate (internal/pages/translations_page.go) and was failing
// this harness with a blank `dir` attribute (a silent 403/redirect, not a
// captured page) until it was routed to the auth till too. "kitchen-stations"
// (ut-docs#516) joined for the same reason — its requireManager gate has no
// UT_AUTH=off bypass either (internal/pages/kitchen_stations_page.go).
// "promotions" (ut-docs#634) joined for the same reason — its GET
// /promotions handler uses the same requireManager closure
// (internal/pages/promotions_page.go), no UT_AUTH=off bypass.
// "country-settings" (ut-docs#659) joined on the same grounds
// (internal/pages/country_settings_page.go).
// "tables" (ut-docs#814) joined for the same reason — its GET /tables
// handler uses the same requireManager closure
// (internal/pages/tables_page.go), no UT_AUTH=off bypass. Found live
// 2026-08-19: this is exactly the blank-`dir`-attribute failure the
// comment above describes, not a new failure mode.
//
// STALE AS OF ut-docs#901/#902 (independent review, 2026-08-23): every page
// above now gates through canPerform(), which HAS the UT_AUTH=off bypass, so
// none of them still *needs* the auth till — "users" moved to
// canPerform(..., "user_management") in ut-docs#556, and the remaining five
// moved to canPerform(..., "settings") in #902. The list is kept as-is on
// purpose, not by omission: the auth till is a fresh, wizard-seeded install,
// whereas the default till is shared with e2e/tests/ specs that leave their
// own rows behind (tables-keyboard-reposition-826.spec.ts creates "E2E Kbd …"
// tables there since #902), which would leak into the manual's screenshots.
// Narrowing this list is therefore a deliberate follow-up decision about
// screenshot fixtures, not a leftover of the auth fix.
const AUTH_TILL_TOPICS = ['users', 'translations', 'kitchen-stations', 'promotions', 'country-settings', 'tables'];

const topics = routedTopics() as { id: string; route: string }[];

for (const topic of topics.filter((t) => !AUTH_TILL_TOPICS.includes(t.id))) {
  for (const locale of LOCALES as string[]) {
    test(`screenshot: ${topic.id} (${locale})`, async ({ page }) => {
      // The basket is server-side state shared across the run — staged once,
      // and only the sell screenshots want it in frame.
      if (topic.id === 'sell') await ensureBasketLines(page);
      if (topic.id === 'invoices') await ensureInvoiceSeller(page);
      await capture(page, topic.id, locale, topic.route);
    });
  }
}

test.describe('manager-gated topics (auth till)', () => {
  test.use({ baseURL: AUTH_BASE });
  for (const id of AUTH_TILL_TOPICS) {
    const topic = topics.find((t) => t.id === id);
    for (const locale of LOCALES as string[]) {
      test(`screenshot: ${id} (${locale})`, async ({ page }) => {
        test.skip(!topic, `${id} topic no longer declares routes`);
        await ensureOperator(page); // fresh Playwright context per test → log in each time
        await capture(page, topic!.id, locale, topic!.route);
      });
    }
  }
});
