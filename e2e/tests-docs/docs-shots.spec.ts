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

// KNOWN, ACCEPTED NON-DETERMINISM (not fixed here — see ut-docs#327's design
// note on this exact class of gap): the `designer` topic's receipt preview
// (#rd-preview, /receipt-designer) bakes the real wall-clock time into its
// sample ticket server-side (internal/pages/receipt_designer.go
// sampleReceiptDoc: `time.Now().Format(...)` in the Meta line, unconditional,
// no override hook). That one line — not the rest of the receipt — legitimately
// differs on every regeneration. Masking would have to hide the whole
// `#rd-preview` block (there's no separate element around just the date), which
// throws away the actual content this topic exists to show, so this is left
// unmasked rather than over-masked. Consequence: `designer/*.png` is expected
// to show a one-line diff on every honest re-run of `make docs-shots`, even
// with no source changes — a real, accepted exception to this harness's
// otherwise-byte-stable guarantee, not a bug in the harness. Fixing it for
// real means giving the designer's preview endpoint a pinned-time override,
// which is a product-code change outside this task's scope (see ut-docs#327
// close-out for the follow-up-card note).

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

  // Let webfonts finish before capturing, or Arabic-script shots race the
  // font swap.
  await page.evaluate(() => (document as any).fonts.ready);

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
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click(); // language
    await page.locator('select[name=country]').selectOption('GB');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click(); // country
    await page.locator('input[name=store_name]').fill('Demo Shop');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click(); // shop name
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click(); // shop type + demo data (ut-docs#539, both optional)
    await page.locator('input[name=pin]').fill(ADMIN_PIN);
    await page.locator('input[name=pin_confirm]').fill(ADMIN_PIN);
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click(); // PIN
    await Promise.all([
      page.waitForURL((u) => !u.pathname.includes('/setup')),
      page.locator('button[type=submit]', { hasText: 'Start selling' }).click(),
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
const AUTH_TILL_TOPICS = ['users', 'translations', 'kitchen-stations'];

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
