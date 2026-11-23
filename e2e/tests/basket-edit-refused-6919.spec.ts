import { test, expect } from './fixtures';

// ut-docs#6919: while a tender (or a card check) runs on the till, the server
// refuses basket edits with 409 `tender_in_flight` and an HX-Trigger
// `tender-in-flight`. The browser shows the basket's own "Payment in progress"
// notice for it, never the generic server-error banner on top.
const BARCODE = '5000000000012';

test.describe('basket edit refused while a payment runs (ut-docs#6919)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a refused basket edit shows the payment notice, not the server-error banner', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.locator('.scan-row input[name="code"]').fill(BARCODE);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // The server's refusal, exactly as writeTenderInFlight sends it.
    await page.route('**/api/pos/remove', (route) => route.fulfill({
      status: 409,
      contentType: 'application/json',
      headers: { 'HX-Reswap': 'none', 'HX-Trigger': '{"tender-in-flight":{}}' },
      body: JSON.stringify({ data: null, error: { code: 'tender_in_flight', message: 'Payment in progress' } }),
    }));
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/remove')),
      // An htmx basket request, as the basket's own controls send it.
      page.evaluate(() => { (window as any).htmx.ajax('POST', '/api/pos/remove', { target: '#basket', swap: 'outerHTML', values: { key: 'x' } }); }),
    ]);

    await expect(page.locator('#tender-busy-notice')).toBeVisible();
    await page.waitForTimeout(300);
    await expect(page.locator('#pos-alert')).toBeHidden();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
  });
});
