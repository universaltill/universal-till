import { test, expect } from './fixtures';
import { watchConsole } from './helpers';
import * as http from 'http';

// ut-docs#4228: the plugin store detail page (/plugins/store/{listing_id})
// has an inline click script (plugins_store_detail.html) that Go tests only
// see as markup. This drives it in a real browser: Download posts the
// listing_id (+ accept_terms), the unverified-publisher confirm() (ADR-0006
// trust surface) gates it (Cancel sends nothing, OK sends), an API error KEY
// is shown as its English text with the buttons re-enabled, and the page of a
// listing with a long description and a long net:validation:<host> permission
// has no sideways scroll at 360/440 px (the phone sweep, 3297, runs on a till
// with no catalogue, so it cannot check this page).
//
// Runs in the `diagnostics` project: run-till-diagnostics.sh points the till
// at 127.0.0.1:8096 (auth off), where this spec starts an in-process fake
// ut-cloud listing two plugins and refusing everything else. No other spec in
// that project opens /plugins*, so the till's 15-minute catalogue cache is
// empty and the first GET fetches from this fake synchronously; the page is
// still loaded in a toPass() reload loop to be safe. (A later spec here that
// opens /plugins would see these two listings for 15 minutes.)
//
// The file name sorts it right after catalog-language-download-4477 and before
// cloud-catalog-bootstrap-4494 / diagnostic-mode-indicator-2169, which register
// this shared till: the cloud-terms box (ut-docs#4791) shows only while the
// till is unregistered, and the first test asserts it gates Download.
//
// POST /api/plugins/store/download is intercepted in the browser and never
// reaches the till: a real download would register the shared diagnostics
// till with the cloud and change the ground for the other specs there.

const CLOUD_PORT = 8096;
const VERIFIED = 'listing-verified-4228';
const UNVERIFIED = 'listing-unverified-4228';
const VENDOR = 'Acme Tillworks Ltd';
const LONG_HOST = 'validation-service-with-an-extremely-long-hostname-for-the-merchant-gateway.eu-central.example-payments-provider.com';
const LONG_DESC = Array.from({ length: 6 }, (_, i) =>
  `Sentence ${i + 1} of a deliberately long description that keeps going so the card has to wrap it ` +
  `across many lines, including Averyveryveryveryveryveryveryveryveryverylongunbrokenwordwithnospaces${i}.`).join(' ');
const NOT_ENTITLED = 'This plugin needs approval before it can be installed — ask your merchant manager to approve it in the marketplace portal.';

function startCatalogCloud(): Promise<http.Server> {
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const url = new URL(req.url ?? '/', `http://127.0.0.1:${CLOUD_PORT}`);
      res.setHeader('Content-Type', 'application/json');
      if (url.pathname === '/v1/catalog/plugins') {
        res.writeHead(200);
        return res.end(JSON.stringify({
          plugins: [
            { id: VERIFIED, listing_id: VERIFIED, name: 'Verified Sample', version: '1.0.0',
              canonical_type: 'report', capabilities: ['report'], trust_tier: 'verified', vendor: 'Good Vendor' },
            { id: UNVERIFIED, listing_id: UNVERIFIED, name: 'Unverified Sample', version: '0.3.0',
              canonical_type: 'report', capabilities: ['report'], vendor: VENDOR, description: LONG_DESC,
              permissions: [`net:validation:${LONG_HOST}`, 'sales:read'] },
          ],
        }));
      }
      res.writeHead(503);
      res.end(JSON.stringify({ data: null, error: { code: 'unavailable', message: 'e2e' } }));
    });
  });
  return new Promise((resolve) => server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(server)));
}

test.describe.serial('Plugin store detail page actions (ut-docs#4228)', () => {
  let cloud: http.Server;
  test.beforeAll(async () => { cloud = await startCatalogCloud(); });
  test.afterAll(async () => { await new Promise<void>((r) => cloud.close(() => r())); });

  async function open(page: import('@playwright/test').Page, id: string, query = '') {
    await expect(async () => {
      const res = await page.goto(`/plugins/store/${id}${query}`);
      expect(res?.status()).toBe(200);
      await expect(page.locator('.store-detail')).toHaveCount(1, { timeout: 1000 });
    }).toPass({ timeout: 20_000 });
  }

  // Records each download POST and answers it with the not_entitled error.
  async function interceptDownload(page: import('@playwright/test').Page) {
    const posts: { method: string; body: string | null }[] = [];
    await page.route('**/api/plugins/store/download', async (route) => {
      posts.push({ method: route.request().method(), body: route.request().postData() });
      await route.fulfill({ status: 403, contentType: 'application/json',
        body: JSON.stringify({ data: null, error: 'plugins.install.error.not_entitled' }) });
    });
    return posts;
  }
  const exempt403 = /^Failed to load resource:.*403/;
  const download = (page: import('@playwright/test').Page) => page.locator('[data-store-action="download"]');

  test('verified listing: terms box gates Download, then it posts with no prompt; the error key is shown', async ({ page }) => {
    const assertClean = watchConsole(page, exempt403);
    const posts = await interceptDownload(page);
    let dialogs = 0;
    page.on('dialog', (d) => { dialogs++; void d.dismiss(); });
    await open(page, VERIFIED);

    // Unregistered till (see the header on file order): the terms box shows
    // and an unticked box stops the Download in the page.
    const terms = page.locator('#store-accept-terms');
    await expect(terms, 'cloud-terms box shown on an unregistered till').toHaveCount(1);
    await download(page).click();
    await expect(page.locator('#store-msg')).toContainText('Accept the Universal Till cloud service terms');
    await page.waitForTimeout(500);
    expect(posts, 'unticked terms box sends nothing').toHaveLength(0);
    await terms.check();
    await download(page).click();
    await expect(page.locator('#store-msg')).toHaveText(`✗ ${NOT_ENTITLED}`);
    await expect(download(page)).toBeEnabled();
    expect(posts).toHaveLength(1);
    expect(posts[0].method).toBe('POST');
    const form = new URLSearchParams(posts[0].body ?? '');
    expect(form.get('listing_id')).toBe(VERIFIED);
    expect(form.get('accept_terms')).toBe('on');
    expect(dialogs, 'a verified publisher is not asked to confirm').toBe(0);
    assertClean();
  });

  test('unverified publisher: Cancel sends nothing, OK sends the POST', async ({ page }) => {
    const assertClean = watchConsole(page, exempt403);
    const posts = await interceptDownload(page);
    await open(page, UNVERIFIED);
    await page.locator('#store-accept-terms').check();

    let message = '';
    page.once('dialog', (d) => { message = d.message(); void d.dismiss(); });
    await download(page).click();
    await page.waitForTimeout(500);
    expect(message).toContain(VENDOR);
    expect(posts, 'Cancel on the trust prompt sends nothing').toHaveLength(0);
    await expect(download(page)).toBeEnabled();

    page.once('dialog', (d) => { message = d.message(); void d.accept(); });
    await download(page).click();
    await expect(page.locator('#store-msg')).toHaveText(`✗ ${NOT_ENTITLED}`);
    expect(message).toContain(VENDOR);
    expect(posts).toHaveLength(1);
    expect(new URLSearchParams(posts[0].body ?? '').get('listing_id')).toBe(UNVERIFIED);
    await expect(download(page)).toBeEnabled();
    assertClean();
  });

  for (const [width, lang] of [[360, 'en'], [440, 'en'], [360, 'fa']] as const) {
    test(`long description + long permission fit at ${width}px (${lang})`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width, height: 800 });
      await open(page, UNVERIFIED, lang === 'en' ? '?lang=en' : `?lang=${lang}`);
      if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      const item = page.locator('.perm-desc-item', { hasText: LONG_HOST });
      await expect(item).toHaveCount(1);
      const fit = await page.evaluate(() => {
        const card = document.querySelector('.store-detail')!.getBoundingClientRect();
        const de = document.documentElement;
        // Each host <code> (the long net:validation: one included), not the
        // <ul>, which is as wide as its container whatever its children do.
        const codes = Array.from(document.querySelectorAll('.perm-desc-item code')).map((c) => c.getBoundingClientRect());
        return { scrollW: de.scrollWidth, clientW: de.clientWidth,
          listInside: codes.length > 0 && codes.every((c) => c.left >= card.left - 1 && c.right <= card.right + 1) };
      });
      expect(fit.scrollW, 'no sideways page scroll').toBeLessThanOrEqual(fit.clientW);
      expect(fit.listInside, 'every permission host stays inside the card').toBe(true);
      await page.screenshot({ path: test.info().outputPath(`store-detail-${width}-${lang}.png`), fullPage: true });
      assertClean();
    });
  }
});
