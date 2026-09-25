import { test, expect } from './fixtures';
import { drainParkedOrders, watchConsole } from './helpers';

// ut-docs#2703 (product owner, 2026-09-25): "when the customer still didn't
// pay for the order and waits for pay at the counter, it should be exactly
// the same as a hold order, not a placed one." Before this, a pay-at-counter
// kiosk order only reached a separate board whose one action was "Mark
// collected" -- no payment, no receipt, no fiscal signature.
//
// The whole journey, driven through the real screens: a customer orders at
// the kiosk in pay-at-counter mode and gets a C-number; the cashier finds
// that order in Open orders, taps it into the basket, takes cash, and gets a
// receipt; the order is gone from Open orders because it is now a paid sale.

test.beforeEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  const res = await page.request.post('/api/settings/kiosk-payment-mode', { form: { mode: 'counter' } });
  expect(res.ok(), 'switching the kiosk to pay-at-counter mode').toBeTruthy();
});

test.afterEach(async ({ page }) => {
  await page.request.post('/api/settings/kiosk-payment-mode', { form: { mode: 'kiosk' } });
  await drainParkedOrders(page.request);
});

test('a pay-at-counter kiosk order is recalled from Open orders and paid as a normal sale', async ({ page, browser }) => {
  // --- The customer, at the kiosk (its own browser context, like the real
  // kiosk device).
  const kioskCtx = await browser.newContext({ hasTouch: true, viewport: { width: 1024, height: 600 } });
  const kiosk = await kioskCtx.newPage();
  let orderNo = '';
  try {
    await kiosk.goto('/self-order/shop');
    await kiosk.locator('.selforder-tile', { hasText: 'Coca-Cola' }).first().click();
    const cart = kiosk.locator('#selforder-cart');
    await expect(cart).toContainText('Coca-Cola');
    await cart.locator('.selforder-checkout').click();
    const modal = kiosk.locator('#selforder-modal');
    await expect(modal).toBeVisible();
    // Pay-at-counter mode: no payment picker, one "Place order" button.
    await modal.locator('button[type=submit]').click();
    const receiptLine = modal.locator('.order-confirmation-receipt');
    await expect(receiptLine).toContainText('C-');
    orderNo = ((await receiptLine.textContent()) ?? '').match(/C-[A-Za-z0-9-]*\d+/)?.[0] ?? '';
    expect(orderNo, 'the customer is shown a C- order number').toMatch(/^C-[A-Za-z0-9-]*\d+$/);
  } finally {
    await kioskCtx.close();
  }

  // --- The cashier, at the till.
  const assertClean = watchConsole(page);
  await page.goto('/open-orders');
  const row = page.locator('[data-testid="open-order-row"]', { hasText: orderNo });
  await expect(row, 'the kiosk order waits in Open orders like a held sale').toBeVisible();
  await expect(row).toContainText('1.20');
  await page.screenshot({ path: test.info().outputPath('open-orders-with-counter-order.png'), fullPage: true });

  await row.click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.20');

  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  const receipt = page.locator('#basket.receipt-view');
  await expect(receipt).toBeVisible();

  // Paid: it has left Open orders for good.
  await page.goto('/open-orders');
  await expect(page.locator('[data-testid="open-order-row"]', { hasText: orderNo })).toHaveCount(0);

  // And it is a real, paid sale under the customer's own number: the Order
  // status board lists completed sales by their display number.
  await page.goto('/orders');
  await expect(page.locator('body')).toContainText(orderNo);
  assertClean();
});
