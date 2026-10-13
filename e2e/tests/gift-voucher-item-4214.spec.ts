import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4214 slice A: selling a gift voucher through a gift_voucher item,
// end to end in a real browser, then spending part of it in a new sale.
//
//  1. A gift voucher item (Type = Gift voucher, price 25.00) is created
//     through the real catalog endpoint.
//  2. Tapping its tile opens the Gift voucher popup with the value
//     prefilled and the card-number field focused; the card number is then
//     "scanned" — fast keystrokes plus Enter into the focused field, the
//     way a USB/HID wedge scanner types — which must submit THIS form, not
//     the sale screen's scan row (app.js's wedge capture, data-scan-own).
//  3. The voucher lands as a basket line showing its number; paying it
//     alone completes the sale and the receipt lists it under "Vouchers
//     issued".
//  4. The same number is refused inline on a second sale (already in use).
//  5. A new sale: goods (itm001, 1.20) + the voucher scanned at the sale
//     screen (a plain number, no GS- prefix) -> pay with the voucher ->
//     its remaining balance is 25.00 - 1.20.
//
// GV_SHOTS_DIR (optional, local review only): writes the popup at the
// 10-inch till size (1024x600) and at phone width (360px) to that folder.

const NAME = `Gift voucher E2E ${Date.now()}`;
const CODE = `77${String(Date.now()).slice(-11)}`;
let itemID = '';

async function openPopup(page) {
  await page.locator('.products-strip-search').click();
  await page.locator('#products-search').fill(NAME);
  const tile = page.locator('#search-results .btn-tile', { hasText: NAME });
  await expect(tile).toBeVisible();
  await tile.click();
  const modal = page.locator('#modifier-modal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('.gift-voucher-popup h3')).toHaveText('Gift voucher');
  return modal;
}

async function scanCode(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

test.describe('gift voucher item: sell, then spend part of it (ut-docs#4214)', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.post('/api/catalog/item', {
      form: { name: NAME, price: '2500', sku: `GV-E2E-${Date.now()}`, isActive: '1', itemType: 'gift_voucher' },
    });
    expect(res.ok(), `create gift voucher item: ${res.status()} ${await res.text()}`).toBeTruthy();
    const m = /data-id="([^"]+)"/.exec(await res.text());
    itemID = m ? m[1] : '';
  });

  test.afterAll(async ({ request }) => {
    await request.post('/api/pos/reset');
    if (itemID) await request.post('/api/catalog/item/deactivate', { form: { id: itemID } });
  });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('scan a card into the popup, pay, receipt; then redeem part of it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const modal = await openPopup(page);
    const number = modal.locator('input[name="gv_code"]');
    await expect(number, 'the card number is focused for the scanner').toBeFocused();
    await expect(modal.locator('[data-gift-voucher-value]'), 'value prefilled from the item price').toHaveValue('25.00');
    await expect(modal.getByRole('button', { name: 'Add', exact: true })).toBeEnabled();
    await expect(modal.getByLabel('Any purchase')).toBeChecked();

    if (process.env.GV_SHOTS_DIR) {
      await page.screenshot({ path: `${process.env.GV_SHOTS_DIR}/gift-voucher-popup-1280.png` });
    }

    // Wedge-scanner style: fast keystrokes, then Enter, into the focused field.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan') && r.request().method() === 'POST'),
      (async () => {
        await page.keyboard.type(CODE, { delay: 5 });
        await page.keyboard.press('Enter');
      })(),
    ]);
    await expect(modal).toBeHidden();
    const line = page.locator('#basket-lines tr', { has: page.locator('.line-name', { hasText: NAME }) });
    await expect(line).toHaveCount(1);
    await expect(line.getByTestId('gift-voucher-number')).toHaveText(CODE);
    await expect(line.locator('.qty-input'), 'a voucher line has no qty control').toHaveCount(0);
    // The scan row did NOT receive the card number.
    await expect(page.locator('#toast-message.error')).toHaveCount(0);

    await page.getByTestId('payment-open').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#payment-overlay').getByTestId('pay-default').click(),
    ]);
    const receipt = page.locator('#basket.receipt-view');
    await expect(receipt).toBeVisible();
    await expect(receipt).toContainText('Vouchers issued');
    await expect(receipt).toContainText(CODE);

    let res = await page.request.get(`/api/vouchers/${CODE}`);
    expect(res.ok()).toBeTruthy();
    expect((await res.json()).data.balance).toBe(2500);

    // The same number again: refused inline, nothing added.
    await page.request.post('/api/pos/reset');
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const again = await openPopup(page);
    await again.locator('input[name="gv_code"]').fill(CODE);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan') && r.request().method() === 'POST'),
      again.getByRole('button', { name: 'Add', exact: true }).click(),
    ]);
    await expect(again).toBeVisible();
    await expect(again.getByRole('alert')).toContainText('already in use');
    await expect(page.locator('#basket-lines tr', { has: page.locator('.line-name', { hasText: NAME }) })).toHaveCount(0);
    await again.getByRole('button', { name: 'Cancel' }).click();
    await expect(again).toBeHidden();

    // A new sale: goods + the voucher scanned at the sale screen.
    await scanCode(page, '5000000000012'); // itm001, 1.20 due
    await scanCode(page, CODE);
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#pay-voucher-btn')).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#pay-voucher-btn').click(),
    ]);
    await expect(page.locator('#basket.receipt-view')).toBeVisible();
    res = await page.request.get(`/api/vouchers/${CODE}`);
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(body.data.balance, 'remaining balance = value - spent').toBe(2500 - 120);
    expect(body.data.status).toBe('active');

    assertClean();
  });

  test('popup layout at the 10-inch till and at phone width', async ({ page }) => {
    for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      const modal = await openPopup(page);
      const box = await modal.boundingBox();
      expect(box, 'popup rendered').not.toBeNull();
      // Fits the viewport horizontally; Add and Cancel are reachable.
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(vp.width + 1);
      for (const name of ['Add', 'Cancel']) {
        const b = await modal.getByRole('button', { name, exact: true }).boundingBox();
        expect(b, `${name} visible at ${vp.width}`).not.toBeNull();
        expect(b!.x + b!.width).toBeLessThanOrEqual(vp.width + 1);
        // Reachable without scrolling the dialog: inside both the viewport
        // and the dialog's own (scrolling) box.
        expect(b!.y + b!.height, `${name} not cut off at ${vp.width}x${vp.height}`).toBeLessThanOrEqual(Math.min(vp.height, box!.y + box!.height) + 1);
        expect(b!.height).toBeGreaterThanOrEqual(40);
      }
      if (process.env.GV_SHOTS_DIR) {
        await page.screenshot({ path: `${process.env.GV_SHOTS_DIR}/gift-voucher-popup-${vp.width}.png` });
      }
      await modal.getByRole('button', { name: 'Cancel' }).click();
      await expect(modal).toBeHidden();
    }
  });
});
