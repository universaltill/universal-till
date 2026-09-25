import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2649: under fa/ar the sell screen's green Pay button showed the
// total in Latin digits ("پرداخت £8.30") while the basket beside it showed
// the same amount in the locale's own digit shapes (£۸٫۳۰). Root cause: the
// page-load render goes through the locale-aware `money` template func
// (correct), but every #basket htmx swap afterward (e.g. adding an item)
// re-labelled the button from window.utCurrency.format(total)
// (web/public/app.js), which has no digit-shape substitution — so in
// practice the button showed Latin digits almost as soon as a real sale
// started. The fix (web/ui/partials/basket.html, web/ui/pages/index.html)
// threads the server-formatted amount through via a new `data-label`
// attribute on #basket's own .total element, which the button's refresh
// IIFE now reads instead of reformatting the total itself.
//
// Demo catalog price (see voucher-scan-pay-amount-1833.spec.ts's own note):
// EAN13 5000000000012 -> itm001 "Coca-Cola Can 330ml", 120 minor units due
// for a single scan -- non-zero, so a Latin-digit regression is visible.

async function scanCode(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

const ASCII_DIGITS = /[0-9]/;

test.describe('Pay button shows locale digits after a basket swap (ut-docs#2649)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const locale of ['fa', 'ar']) {
    test(`${locale}: payment-open button matches the basket total's locale-formatted digits`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto(`/?lang=${locale}`);
      await page.waitForSelector('.pos-container');

      // Adding an item is an htmx #basket swap -- exactly the path the bug
      // report identifies as re-labelling the button from JS instead of the
      // server-rendered amount.
      await scanCode(page, '5000000000012');

      const totalLabel = await page.locator('#basket .total').getAttribute('data-label');
      expect(totalLabel, 'basket .total data-label present').toBeTruthy();
      expect(totalLabel).not.toMatch(ASCII_DIGITS);

      const payButtonText = await page.getByTestId('payment-open').innerText();
      expect(payButtonText).toContain(totalLabel!);
      expect(payButtonText).not.toMatch(ASCII_DIGITS);

      assertClean();
    });
  }
});
