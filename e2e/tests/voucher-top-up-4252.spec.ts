import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4252: topping up an existing gift voucher as a sale, end to end in
// a real browser. What the Go handler tests cannot see:
//  1. the detail page's Top up form adds the line to the CASHIER's basket and
//     lands on the sale screen with it tagged "Top-up";
//  2. paying it shows the top-up on the on-screen receipt — the label, the
//     added amount and the new balance stacked, not overlapping (the
//     Tester's driven run found the label drawn on top of the number);
//  3. the voucher page then shows the new balance and a Top-up history row
//     linking to the sale;
//  4. a refusal is an inline message, never a modal, and at phone width the
//     section never pushes the page sideways (en and fa/RTL).

const RUN = Date.now().toString(36).toUpperCase();
const ITEM = `Gift voucher T4252 ${RUN}`;
const CODE = `T4252-${RUN}`;
let itemID = '';

test.describe.serial('gift voucher top-up as a sale (ut-docs#4252)', () => {
  test.beforeAll(async ({ request }) => {
    // The top-up line is based on the shop's gift voucher item.
    const res = await request.post('/api/catalog/item', {
      form: { name: ITEM, price: '2500', sku: `GV-T4252-${RUN}`, isActive: '1', itemType: 'gift_voucher' },
    });
    expect(res.ok(), `create gift voucher item: ${res.status()} ${await res.text()}`).toBeTruthy();
    const m = /data-id="([^"]+)"/.exec(await res.text());
    itemID = m ? m[1] : '';
  });

  test.afterAll(async ({ request }) => {
    await request.post('/api/pos/reset');
    if (itemID) await request.post('/api/catalog/item/deactivate', { form: { id: itemID } });
  });

  test('import a voucher (12.00) to top up', async ({ page }) => {
    await page.goto('/vouchers?lang=en');
    await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
      name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${CODE},12.00,Spec holder\n`),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
      page.locator('#vouchers-import-form button[type=submit]').click(),
    ]);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
      page.locator('#vouchers-import-result button[type=submit]').click(),
    ]);
    await expect(page.locator('#vouchers-import-result')).toContainText('1 voucher(s) imported');
  });

  test('a refused amount is an inline message; the basket is untouched', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const sec = page.locator('#voucher-top-up');
    await sec.locator('input[name=amount]').fill('0');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/top-up') && r.status() === 400),
      sec.getByRole('button', { name: 'Add to sale' }).click(),
    ]);
    await expect(page.locator('#voucher-top-up-msg [role=alert]')).toContainText('Enter an amount above zero');
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    expect(new URL(page.url()).pathname).toBe(`/vouchers/${CODE}`);
    await page.goto('/');
    await expect(page.getByTestId('gift-voucher-top-up')).toHaveCount(0);
  });

  test('top up 10.50: basket line, receipt with the new balance, history row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const sec = page.locator('#voucher-top-up');
    await expect(sec.getByRole('heading')).toHaveText(/Top up/i);
    await sec.getByLabel(/Amount to add/).fill('10.50');
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/'),
      sec.getByRole('button', { name: 'Add to sale' }).click(),
    ]);
    await page.waitForSelector('.pos-container');

    const line = page.locator('#basket-lines tr', { has: page.getByTestId('gift-voucher-top-up') });
    await expect(line).toHaveCount(1);
    await expect(line.getByTestId('gift-voucher-number')).toHaveText(CODE);
    await expect(line.getByTestId('gift-voucher-top-up')).toHaveText('Top-up');
    await expect(line).toContainText('10.50');

    await page.getByTestId('payment-open').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#payment-overlay').getByTestId('pay-default').click(),
    ]);
    const block = page.locator('#basket.receipt-view .receipt-vouchers');
    await expect(block).toBeVisible();
    await expect(block).toContainText('Gift voucher top-up');
    await expect(block).toContainText('+');
    await expect(block.locator('.receipt-voucher-balance')).toContainText('22.50');

    // Label, then number + amount, then the new balance — stacked, never
    // drawn over each other (the receipt pane is narrow at 1024x600).
    const label = (await block.locator('.receipt-voucher-top-up').boundingBox())!;
    const code = (await block.locator('.receipt-voucher-code').boundingBox())!;
    const balance = (await block.locator('.receipt-voucher-balance').boundingBox())!;
    expect(label.y + label.height, 'top-up label sits above the number').toBeLessThanOrEqual(code.y + 1);
    expect(balance.y, 'new balance sits under the number').toBeGreaterThanOrEqual(code.y + code.height - 1);
    const labelLines = await block.locator('.receipt-voucher-top-up').evaluate(
      (el) => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight || '20')),
    );
    expect(labelLines, 'the label is not squeezed into a one-word column').toBeLessThanOrEqual(1);

    const res = await page.request.get(`/api/vouchers/${CODE}`);
    expect((await res.json()).data.balance).toBe(2250);

    await page.request.post('/api/pos/reset');
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const row = page.locator('table tbody tr', { hasText: 'Top-up' });
    await expect(row).toHaveCount(1);
    await expect(row).toContainText('+');
    await expect(row).toContainText('10.50');
    await expect(row.locator('a')).toHaveAttribute('href', /^\/journal\//);
    assertClean();
  });

  for (const lang of ['en', 'fa']) {
    test(`the Top up section at 360px does not scroll sideways (${lang})`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto(`/vouchers/${CODE}?lang=${lang}`);
      await expect(page.locator('#voucher-top-up')).toBeVisible();
      const vw = await page.evaluate(() => document.documentElement.clientWidth);
      const scrollW = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(scrollW, 'page wider than the viewport').toBeLessThanOrEqual(vw + 1);
      for (const sel of ['#voucher-top-up input[name=amount]', '#voucher-top-up button[type=submit]']) {
        const b = (await page.locator(sel).boundingBox())!;
        expect(b.x, `${sel} starts on screen`).toBeGreaterThanOrEqual(-1);
        expect(b.x + b.width, `${sel} ends on screen`).toBeLessThanOrEqual(vw + 1);
      }
    });
  }
});
