import { test, expect } from './fixtures';

// ut-docs#6909: while a tender request is pending, a scan or tile tap must not
// start (hx-sync="#basket:replace" would abort the tender xhr, so the receipt
// never swaps in, and the scanned line would be wiped by the sale's Reset).
// The gate lives in app.js (htmx:confirm); the server also refuses a second
// tender (409) and a scan (error notice).
const BARCODE = '5000000000012';

test.describe('tender in-flight gate (ut-docs#6909)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  async function scanFirst(page) {
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.locator('.scan-row input[name="code"]').fill(BARCODE);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
  }

  test('a scan during a slow tender is held back, shows a notice, and the sale completes', async ({ page }) => {
    await page.route('**/api/pos/tender', (route) => setTimeout(() => route.continue(), 2500));
    const scans: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/pos/scan') && r.method() === 'POST') scans.push(r.url()); });
    await scanFirst(page);
    scans.length = 0;

    await page.getByTestId('payment-open').click();
    await page.locator('#payment-overlay').getByTestId('pay-default').click();

    // Mid-tender: type a code and submit it.
    await page.locator('.scan-row input[name="code"]').fill(BARCODE);
    await page.locator('.scan-row button[type=submit]').dispatchEvent('click');
    await expect(page.locator('#tender-busy-notice')).toBeVisible();
    await expect(page.locator('#basket')).toHaveAttribute('aria-busy', 'true');
    expect(await page.locator('dialog[open]:modal').count(), 'no modal dialog is opened by the notice').toBe(0);
    expect(scans, 'no scan request is sent while the tender runs').toHaveLength(0);
    // The refused code stays in the scan box (the clear callbacks run on a 0 ms timer).
    await page.waitForTimeout(300);
    await expect(page.locator('.scan-row input[name="code"]')).toHaveValue(BARCODE);

    // The tender is not aborted: the receipt view swaps in.
    await expect(page.locator('#basket.receipt-view')).toBeVisible({ timeout: 15_000 });
    expect(scans, 'still no scan request after the sale').toHaveLength(0);
  });

  test('an aborted tender request re-reads the basket (resync)', async ({ page }) => {
    await scanFirst(page);
    const basketGets: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/ui/basket')) basketGets.push(r.url()); });
    await page.route('**/api/pos/tender', (route) => route.abort('connectionreset'));

    await page.getByTestId('payment-open').click();
    await page.locator('#payment-overlay').getByTestId('pay-default').click();
    await expect.poll(() => basketGets.length, 'the client re-reads /ui/basket after the abort').toBeGreaterThan(0);
  });
});
