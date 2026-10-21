import { test, expect } from './fixtures';
import { drainParkedOrders } from './helpers';

// ut-docs#4714: a kiosk customer who picked their own language and pays at
// the counter gets their receipt in that language when the cashier taps
// Print on the receipt screen, not only when it prints by itself (#4705).
// Driven through the real screens: the customer picks Turkish at the kiosk
// and places a pay-at-counter order; the cashier recalls it, takes cash and
// taps Print. The Print button carries the customer's language and the
// print request sends it. A sale rung up on the till sends none.

test.beforeEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  const res = await page.request.post('/api/settings/kiosk-payment-mode', { form: { mode: 'counter' } });
  expect(res.ok(), 'switching the kiosk to pay-at-counter mode').toBeTruthy();
  // No printer in the e2e till: the receipt screen falls back to the
  // browser's print dialog, which must not open in a headless run.
  await page.addInitScript(() => { window.print = () => {}; });
});

test.afterEach(async ({ page }) => {
  await page.request.post('/api/settings/kiosk-payment-mode', { form: { mode: 'kiosk' } });
  await drainParkedOrders(page.request);
});

test('Print on the receipt screen sends the kiosk customer\'s language', async ({ page, browser }) => {
  const kioskCtx = await browser.newContext({ hasTouch: true, viewport: { width: 1024, height: 600 } });
  const kiosk = await kioskCtx.newPage();
  let orderNo = '';
  try {
    await kiosk.goto('/self-order');
    await kiosk.locator('nav.selforder-langs a[lang="tr"]').click();
    await expect(kiosk.locator('html')).toHaveAttribute('lang', 'tr');
    await kiosk.goto('/self-order/shop');
    await kiosk.locator('.selforder-tile', { hasText: 'Coca-Cola' }).first().click();
    const cart = kiosk.locator('#selforder-cart');
    await expect(cart).toContainText('Coca-Cola');
    await cart.locator('.selforder-checkout').click();
    const modal = kiosk.locator('#selforder-modal');
    await expect(modal).toBeVisible();
    await modal.locator('button[type=submit]').click();
    const receiptLine = modal.locator('.order-confirmation-receipt');
    await expect(receiptLine).toContainText('C-');
    orderNo = ((await receiptLine.textContent()) ?? '').match(/C-[A-Za-z0-9-]*\d+/)?.[0] ?? '';
    expect(orderNo).toMatch(/^C-[A-Za-z0-9-]*\d+$/);
  } finally {
    await kioskCtx.close();
  }

  await page.goto('/open-orders?tab=counter');
  await page.locator('[data-testid="open-order-row"]', { hasText: orderNo }).click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  const receipt = page.locator('#basket.receipt-view');
  await expect(receipt).toBeVisible();

  // ut-docs#4730: the browser-print fallback prints the page, so the page
  // carries a print-only copy in the customer's language: hidden on screen,
  // and the only receipt card shown in print media.
  const screenCard = receipt.locator('.receipt-card:not(.receipt-print-copy)');
  const printCopy = receipt.locator('.receipt-print-copy');
  await expect(printCopy).toHaveAttribute('lang', 'tr');
  await expect(printCopy).toBeHidden();
  await expect(screenCard).toBeVisible();
  await page.emulateMedia({ media: 'print' });
  await expect(printCopy).toBeVisible();
  await expect(printCopy).toContainText('Fiş #');
  await expect(screenCard).toBeHidden();
  await page.emulateMedia({ media: 'screen' });

  const print = receipt.locator('[data-action^="receipt-print:"]');
  await expect(print).toHaveAttribute('data-customer-locale', 'tr');
  const [req] = await Promise.all([
    page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('/api/print/receipt/')),
    print.click(),
  ]);
  expect(req.postData() ?? '').toBe('customer_locale=tr');
});

test('Print on a till sale\'s receipt screen sends no customer language', async ({ page }) => {
  await page.goto('/');
  await page.request.post('/api/pos/reset');
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  const receipt = page.locator('#basket.receipt-view');
  await expect(receipt).toBeVisible();

  const print = receipt.locator('[data-action^="receipt-print:"]');
  await expect(print).not.toHaveAttribute('data-customer-locale', /.*/);
  const [req] = await Promise.all([
    page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('/api/print/receipt/')),
    print.click(),
  ]);
  expect(req.postData() ?? '').toBe('');
});
