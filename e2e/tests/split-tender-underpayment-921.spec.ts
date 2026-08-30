import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#921 review finding: the split-tender panel's submitPayments()
// (web/public/app.js) used to branch success/failure on response.ok alone.
// The server's rejection path (insufficient stock, underpayment, the fiscal
// hard gate, ...) renders as 200 + an in-basket error toast, not a 4xx --
// checking response.ok read a rejected tender as success, wiped the
// operator's pending split payments, and declared "Sale completed." on a
// sale that never happened. This pins the fix: a real underpayment
// submitted through the real split-tender UI must show the error notice and
// must NEVER show the success status, and the sale must not be recorded.
test.describe('split-tender panel does not report success on a rejected tender (ut-docs#921)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('underpayment via the Split tab shows the error toast, never "Sale completed."', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    // ut-docs#1252: the Pay/Split tabs now live inside the #payment-overlay
    // dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();
    await page.locator('#split-tender-form select[name="method"]').selectOption({ index: 0 });
    // Well short of the total on purpose -- this IS the split-tender
    // panel's characteristic failure (it exists to accumulate partial
    // payments, and completing before covering the total is the natural
    // mistake it's meant to catch, not paper over).
    await page.locator('#split-tender-form input[name="amount"]').fill('0.50');
    await page.locator('#split-tender-add').click();
    await page.waitForSelector('.payment-pill');

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#split-tender-submit').click(),
    ]);

    // The rejection must render as the persistent error notice inside the
    // basket -- same surface the insufficient-stock/fiscal rejections use.
    await expect(page.locator('#toast-message.error')).toBeVisible();

    // The bug this pins: the split-tender panel's OWN status line must
    // never say the sale completed on a rejection, and the operator's
    // pending payment pill must survive (payments = [] only runs on the
    // real success path).
    await expect(page.locator('#split-tender-status')).not.toContainText('Sale completed');
    await expect(page.locator('.payment-pill')).toHaveCount(1);

    // And the basket itself must survive -- same invariant the Go-level
    // TestTenderHandler_UnderpaymentShowsLocalizedToastNotRawError already
    // pins server-side, checked here end-to-end through the real browser.
    await expect(page.locator('.basket table tbody tr')).toHaveCount(1);

    assertClean();
  });

  // Companion to the rejection case above: the fix re-queries the swapped
  // DOM for an error notice before declaring success, so a genuinely
  // covered payment must still show "Sale completed." -- pinning that the
  // fix didn't flip the false-positive into a false-negative instead.
  test('a payment that covers the total via the Split tab shows "Sale completed."', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    // ut-docs#1252: the Pay/Split tabs now live inside the #payment-overlay
    // dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();
    await page.locator('#split-tender-form select[name="method"]').selectOption({ index: 0 });
    await page.locator('#split-tender-fill').click(); // fills the exact remaining total
    await page.locator('#split-tender-add').click();
    await page.waitForSelector('.payment-pill');

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#split-tender-submit').click(),
    ]);

    await expect(page.locator('#split-tender-status')).toContainText('Sale completed.');
    await expect(page.locator('#toast-message.error')).toHaveCount(0);
    await expect(page.locator('.payment-pill')).toHaveCount(0); // cleared on real success
    await expect(page.locator('.basket table tbody tr')).toHaveCount(0); // basket reset for the next sale

    assertClean();
  });
});
