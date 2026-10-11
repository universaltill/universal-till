import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4071: window.utCurrency.format always grouped thousands in threes,
// so under en-IN / ur-PK an amount >= 100,000 major units read
// "£1,000,000.00" client-side next to the server basket's "£10,00,000.00".
// utCurrency now reads <body data-number-grouping> (the server's
// `numbergrouping` func) and groups Indian-style (last 3 digits, then pairs).
//
// The literals below are pinned on the Go side by
// TestFormatMoneyDisplay_IndianGrouping4071 (internal/httpx/currency_test.go).
// Demo catalog: EAN13 5000000000012 -> itm001, 120 minor units due.

async function scanCode(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

const fmt = (page, minor: number) =>
  page.evaluate((m) => (window as any).utCurrency.format(m), minor);

test.describe('client-side money groups like the server (ut-docs#4071)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const cases = [
    { locale: 'en-IN', million: '£10,00,000.00', big: '£12,34,56,789.00' },
    { locale: 'ur-PK', million: '£۱۰٬۰۰٬۰۰۰٫۰۰', big: '£۱۲٬۳۴٬۵۶٬۷۸۹٫۰۰' },
  ];

  for (const c of cases) {
    test(`${c.locale}: Indian grouping above 100,000 major units`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto(`/?lang=${c.locale}`);
      await page.waitForSelector('.pos-container');
      await scanCode(page, '5000000000012');

      // Sanity: the page really runs this locale.
      const serverLabel = await page.locator('#basket .total').getAttribute('data-label');
      expect(serverLabel).toBeTruthy();
      expect(await fmt(page, 120)).toBe(serverLabel);

      expect(await fmt(page, 100000000)).toBe(c.million);
      if (c.big) expect(await fmt(page, 12345678900)).toBe(c.big);

      assertClean();
    });
  }

  test('en control: three-digit grouping unchanged', async ({ page }) => {
    await page.goto('/?lang=en');
    await page.waitForSelector('.pos-container');
    expect(await fmt(page, 100000000)).toBe('£1,000,000.00');
  });
});
