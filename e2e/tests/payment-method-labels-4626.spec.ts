import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4626: payment-method ids ("cash", "card", ...) used to show raw in
// the Split tab's method <select>, its pending-payment pills and the "added"
// status. They now read the translated method name. Assertions are POSITIVE
// (the exact fa label), see the note atop split-tender-i18n-925.spec.ts.
const FA = { cash: 'نقدی', card: 'کارت' };

test.describe('split-tender method labels localize (ut-docs#4626)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('fa: select options, pending pill and added status show Persian labels', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await page.waitForSelector('.pos-container');
    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab').nth(1).click();

    const select = page.locator('#split-tender-method');
    // Option VALUES stay the ids (what the server receives); the visible
    // TEXT is the translated name.
    await expect(select.locator('option[value="cash"]')).toHaveText(FA.cash);
    await expect(select.locator('option[value="card"]')).toHaveText(FA.card);

    await select.selectOption('cash');
    await page.locator('#split-tender-form input[name="amount"]').fill('1.20');
    await page.locator('#split-tender-add').click();
    await page.waitForSelector('.payment-pill');

    await expect(page.locator('.payment-pill strong').first()).toHaveText(FA.cash);
    await expect(page.locator('#split-tender-status')).toContainText(FA.cash);

    assertClean();
  });
});
