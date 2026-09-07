import { test, expect } from './fixtures';

// ut-docs#1624: New Customer (.tender-footer.single, inside #payment-overlay)
// used to close the overlay unconditionally via a synchronous onclick that
// ran BEFORE the /api/pos/reset request even completed -- unlike every New
// Sale button in this same file (index.html), which only closes the overlay
// after a successful response with no inline #toast-message.error. An
// operator tapping New Customer against an errored reset lost the Tender
// panel immediately and never saw the error. The fix moves New Customer to
// the same hx-on::after-request gate New Sale already uses; this spec drives
// both sides of that gate for New Customer specifically.
//
// /api/pos/reset has no real failure path today (Engine.Reset() cannot
// error), so the error case is driven via route interception -- following
// the established pattern in printer-discovery-http-error-1556.spec.ts --
// fulfilling with the exact #basket outerHTML fragment the real handler
// would render on an error toast (web/ui/partials/basket.html's
// `.pos-notice.error#toast-message` shape), since hx-swap="outerHTML" swaps
// the whole #basket element.
test.describe('New Customer respects the payment-overlay close gate (ut-docs#1624)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const openOverlayWithItem = async (page: import('@playwright/test').Page) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
  };

  test('New Customer closes the overlay on a successful reset (unchanged behavior)', async ({ page }) => {
    await openOverlayWithItem(page);

    const newCustomerBtn = page.locator('.tender-footer.single .btn', { hasText: 'New Customer' });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      newCustomerBtn.click(),
    ]);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
  });

  test('New Customer leaves the overlay open when the reset response carries an error toast', async ({ page }) => {
    await openOverlayWithItem(page);

    // Intercept only the reset this test drives -- fulfil with the same
    // #basket outerHTML fragment shape the real error-toast render produces
    // (web/ui/partials/basket.html), so the client sees exactly what it
    // would from a genuinely failing reset.
    await page.route('**/api/pos/reset', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: '<div class="basket" id="basket"><div class="pos-notice error" id="toast-message" role="alert"><span class="notice-text">simulated reset failure</span><button type="button" class="notice-dismiss">✕</button></div></div>',
      });
    });

    const newCustomerBtn = page.locator('.tender-footer.single .btn', { hasText: 'New Customer' });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      newCustomerBtn.click(),
    ]);
    await expect(page.locator('#toast-message.error')).toBeVisible();
    await expect(page.locator('#payment-overlay')).toBeVisible();
  });
});
