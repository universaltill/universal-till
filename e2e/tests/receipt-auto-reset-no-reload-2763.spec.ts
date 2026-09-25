import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2763 (product owner, p1): "every few minutes the till app
// refreshes and I can see the screen refresh". The receipt partial armed a
// 60 s `window.location='/'` that nothing ever cancelled, so 60 s after
// every sale the whole page reloaded -- usually in the middle of the NEXT
// sale. The receipt's auto-reset (and its New Customer button) now return
// to a fresh sale by swapping #basket only, and the countdown dies the
// moment the cashier starts the next sale.
//
// A window-level marker is the navigation detector: any reload or
// navigation builds a new window and loses it.

type Page = import('@playwright/test').Page;

async function scanCoke(page: Page) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
}

async function completeSale(page: Page) {
  await scanCoke(page);
  await page.getByTestId('payment-open').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.locator('#payment-overlay').getByTestId('pay-default').click(),
  ]);
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
}

async function markWindow(page: Page) {
  await page.evaluate(() => { (window as unknown as { __ut2763: string }).__ut2763 = 'alive'; });
}

async function windowMarker(page: Page) {
  return page.evaluate(() => (window as unknown as { __ut2763?: string }).__ut2763 ?? null);
}

test.describe('receipt auto-reset never reloads the page (ut-docs#2763)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    // Fake timers, running in real time until fast-forwarded.
    await page.clock.install();
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container .products .btn-tile');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('starting the next sale cancels the receipt countdown: no reload 60 s later, the new basket stays', async ({ page }) => {
    const assertClean = watchConsole(page);
    await completeSale(page);
    await markWindow(page);

    // The next customer arrives within the minute.
    await page.clock.fastForward(10_000);
    await scanCoke(page);
    await expect(page.locator('#basket.receipt-view')).toHaveCount(0);

    // Well past the old 60 s timer.
    await page.clock.fastForward(70_000);
    await page.waitForTimeout(500);

    expect(await windowMarker(page), 'the page must not have navigated/reloaded').toBe('alive');
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#basket.receipt-view')).toHaveCount(0);
    assertClean();
  });

  test('an idle receipt still resets to an empty sale after 60 s, by a basket swap, not a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await completeSale(page);
    await markWindow(page);

    await page.clock.fastForward(30_000);
    await expect(page.locator('#basket.receipt-view')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/basket')),
      page.clock.fastForward(31_000),
    ]);
    await expect(page.locator('#basket.receipt-view')).toHaveCount(0);
    await expect(page.locator('#basket')).toBeVisible();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    expect(await windowMarker(page), 'the reset must be a swap, never a navigation').toBe('alive');
    assertClean();
  });

  test('New Customer returns to an empty sale by a basket swap, not a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await completeSale(page);
    await markWindow(page);

    await page.locator('#basket.receipt-view').getByRole('button', { name: 'New Customer' }).click();
    await expect(page.locator('#basket.receipt-view')).toHaveCount(0);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    expect(await windowMarker(page)).toBe('alive');

    // …and the dead receipt's countdown does not fire later either.
    await page.clock.fastForward(70_000);
    await page.waitForTimeout(300);
    expect(await windowMarker(page)).toBe('alive');
    assertClean();
  });
  test('leaving the sell screen through the app shell kills the countdown: nothing fires 60 s later', async ({ page }) => {
    const assertClean = watchConsole(page);
    await completeSale(page);
    await markWindow(page);

    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    await expect(page.locator('#basket')).toHaveCount(0);

    let basketFetched = false;
    page.on('request', (r) => { if (r.url().includes('/ui/basket')) basketFetched = true; });
    await page.clock.fastForward(70_000);
    await page.waitForTimeout(300);

    expect(await windowMarker(page), 'a shell navigation is a swap; the old receipt timer must not reload it').toBe('alive');
    await expect(page).toHaveURL(/\/menu$/);
    expect(basketFetched, 'the dead receipt must not fetch the basket').toBe(false);
    assertClean();
  });

  test('touching the receipt restarts the minute instead of resetting under the finger', async ({ page }) => {
    await completeSale(page);
    await page.clock.fastForward(50_000);
    await page.locator('#basket.receipt-view .receipt-lines').click();
    await page.clock.fastForward(20_000);
    await page.waitForTimeout(300);
    await expect(page.locator('#basket.receipt-view'), 'restarted: 20 s after the touch is not a minute').toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/basket')),
      page.clock.fastForward(41_000),
    ]);
    await expect(page.locator('#basket.receipt-view')).toHaveCount(0);
  });
});
