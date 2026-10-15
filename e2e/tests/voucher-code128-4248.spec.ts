import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4248: the on-screen receipt draws an issued voucher's code as a
// CODE128 barcode, like the printed receipt, so a pre-printed card number
// with lowercase letters or more than CODE39's 32 characters still gets a
// scannable barcode (before, it showed none).
//
// BARCODE_SHOT (optional, local review only): writes the barcode element to
// that PNG path so it can be run through a real decoder.

const NAME = `Gift voucher C128 ${Date.now()}`;
// Lowercase letters, which CODE39 can't carry: 25 chars, e.g. "gc-1760524800000-abcdefgh".
const CODE = `gc-${Date.now()}-abcdefgh`;
let itemID = '';

test.describe('issued voucher code as a CODE128 barcode on the on-screen receipt (ut-docs#4248)', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.post('/api/catalog/item', {
      form: { name: NAME, price: '1000', sku: `GV-C128-${Date.now()}`, isActive: '1', itemType: 'gift_voucher' },
    });
    expect(res.ok(), `create gift voucher item: ${res.status()} ${await res.text()}`).toBeTruthy();
    const m = /data-id="([^"]+)"/.exec(await res.text());
    itemID = m ? m[1] : '';
  });

  test.afterAll(async ({ request }) => {
    await request.post('/api/pos/reset');
    if (itemID) await request.post('/api/catalog/item/deactivate', { form: { id: itemID } });
  });

  test('a lowercase card number gets a barcode that fits the receipt', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill(NAME);
    await page.locator('#search-results .btn-tile', { hasText: NAME }).click();
    const modal = page.locator('#modifier-modal');
    await expect(modal.locator('input[name="gv_code"]')).toBeFocused();
    await modal.locator('input[name="gv_code"]').fill(CODE);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan') && r.request().method() === 'POST'),
      modal.getByRole('button', { name: 'Add', exact: true }).click(),
    ]);
    await expect(modal).toBeHidden();

    await page.getByTestId('payment-open').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#payment-overlay').getByTestId('pay-default').click(),
    ]);
    const receipt = page.locator('#basket.receipt-view');
    await expect(receipt).toContainText(CODE);
    const bars = receipt.locator('.receipt-vouchers .barcode svg');
    await expect(bars).toHaveCount(1);
    await expect(bars).toHaveAttribute('aria-label', CODE);
    // It scales to the receipt instead of overflowing it.
    const barBox = (await bars.boundingBox())!;
    const listBox = (await receipt.locator('.receipt-vouchers ul').boundingBox())!;
    expect(barBox.width).toBeGreaterThan(0);
    expect(barBox.x + barBox.width).toBeLessThanOrEqual(listBox.x + listBox.width + 1);
    if (process.env.BARCODE_SHOT) {
      await bars.screenshot({ path: process.env.BARCODE_SHOT });
    }
    assertClean();
  });
});
