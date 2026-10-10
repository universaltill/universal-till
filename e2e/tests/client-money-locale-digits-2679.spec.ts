import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2679: under fa/ar, client-rendered money (window.utCurrency.format:
// the card-fee hint, the voucher "pay with" button, the split-tender panel
// messages) showed Latin digits ("£1.20") next to a server-rendered basket
// in the locale's own numerals ("£۱٫۲۰"). utCurrency now shapes the number
// part from <body data-number-digits>, mirroring Go's LocalizeDigits.
//
// The fee hint (app.js refresh() -> utCurrency.format(fee)) needs a payment
// method with a configured fee and is not exercised separately here: it is
// the same single utCurrency.format path asserted below.
//
// Demo catalog: EAN13 5000000000012 -> itm001, 120 minor units due.

async function scanCode(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function issueVoucher(page, code: string, balanceMinor: number) {
  const res = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: {
      payments: [{ method: 'cash', amount: balanceMinor }],
      issue_vouchers: [{ amount: balanceMinor, code }],
    },
  });
  expect(res.ok(), `issue voucher ${code}: ${res.status()} ${await res.text()}`).toBeTruthy();
}

const ASCII_DIGITS = /[0-9]/;

test.describe('client-side money is digit-shaped like the server (ut-docs#2679)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const locale of ['fa', 'ar']) {
    test(`${locale}: utCurrency.format(120) equals the server basket total, no ASCII digits`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto(`/?lang=${locale}`);
      await page.waitForSelector('.pos-container');
      await scanCode(page, '5000000000012');

      const serverLabel = await page.locator('#basket .total').getAttribute('data-label');
      expect(serverLabel).toBeTruthy();
      const client = await page.evaluate(() => (window as any).utCurrency.format(120));
      expect(client).not.toMatch(ASCII_DIGITS);
      expect(client).toBe(serverLabel);

      // Parse/prefill helpers must stay ASCII: they feed editable inputs.
      const major = await page.evaluate(() => (window as any).utCurrency.toMajor(120));
      expect(major).toMatch(/^[0-9]+[.,][0-9]+$/);

      assertClean();
    });

    test(`${locale}: voucher pay button label has no ASCII digits`, async ({ page }) => {
      const assertClean = watchConsole(page);
      const code = `GS-E2E-DIG-${locale}-${Date.now()}`;
      await issueVoucher(page, code, 5000);

      await page.goto(`/?lang=${locale}`);
      await page.waitForSelector('.pos-container');
      await scanCode(page, '5000000000012');
      await scanCode(page, code);

      await page.getByTestId('payment-open').click();
      await expect(page.locator('#payment-overlay')).toBeVisible();
      const btn = page.getByTestId('pay-voucher');
      await expect(btn).toBeVisible();
      const label = await btn.innerText();
      // The label quotes the voucher code (Latin by design); strip it and
      // check the money part only.
      expect(label.replace(code, '')).not.toMatch(ASCII_DIGITS);

      assertClean();
    });
  }

  test('en control: Latin digits unchanged', async ({ page }) => {
    await page.goto('/?lang=en');
    await page.waitForSelector('.pos-container');
    const client = await page.evaluate(() => (window as any).utCurrency.format(120));
    expect(client).toBe('£1.20');
  });
});
