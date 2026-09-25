import { test, expect } from './fixtures';
import { watchConsole, clearAllHeldSales } from './helpers';

// ut-docs#2702 (product owner): "the buttons on the right in the sell page
// ... are big and a lot, barcode box can be thinner smaller with its
// belongings, one row buttons at the bottom of it is enough: one payment
// button which opens the payment tab and icons for holding and new sell
// and open orders". The tender panel is now a thin scan row plus ONE action
// row. tender-panel-reachable.spec.ts hit-tests that row at every supported
// viewport; this spec drives what each control DOES, and the space the
// change was meant to give back to the product grid.

const ACTION_ROW = ['payment-open', 'tender-footer-hold', 'kiosk-checkout-start', 'parked-orders-open'];

async function scanCoke(page: import('@playwright/test').Page) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
}

test.describe('compact tender panel (ut-docs#2702)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('the tender panel is two thin rows and the product grid gets the height back at 1280x800', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container .products .btn-tile');

    // In rem, not px: the root font-size is fluid (it grows with the
    // viewport), so 2.5rem is ~45px here and ~40px at 1024x600.
    const g = await page.evaluate(() => {
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
      const h = (sel: string) => document.querySelector(sel)!.getBoundingClientRect().height / rem;
      return {
        tender: h('.pos-container > .tender'), scan: h('.scan-row'), row: h('.tender-default-footer'),
        code: h('.scan-row input[name="code"]'), products: h('.pos-container > .products'),
        column: h('.pos-container'),
      };
    });
    expect(g.code, 'scan field is the thin 2.5rem box').toBeLessThanOrEqual(2.55);
    expect(g.scan, 'scan row is one line of 2.75rem controls').toBeLessThanOrEqual(2.8);
    expect(g.row, 'action row is one line of 2.75rem controls').toBeLessThanOrEqual(2.8);
    // Two 2.75rem rows + the .45rem gap between them + the panel's own
    // .7rem padding each side and 1px borders: content-sized, nothing else.
    expect(g.tender, 'tender panel is content-sized (two rows)').toBeLessThanOrEqual(7.6);
    // Products gets the whole column but for tender + the grid row-gap.
    expect(g.products, 'the freed height goes to the product grid').toBeGreaterThanOrEqual(g.column - g.tender - 1);
    assertClean();
  });

  test('every icon control is a 44px touch target with an accessible name and a tooltip', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    for (const [id, name] of [
      ['tender-footer-hold', 'Hold Sale'],
      ['kiosk-checkout-start', 'New Sale'],
      ['parked-orders-open', /^Open orders/],
    ] as const) {
      const el = page.getByTestId(id);
      await expect(el).toHaveAccessibleName(name);
      await expect(el).toHaveAttribute('title', /\S/);
      const b = (await el.boundingBox())!;
      expect(b.width, `${id} width`).toBeGreaterThanOrEqual(44);
      expect(b.height, `${id} height`).toBeGreaterThanOrEqual(44);
    }
    const add = page.locator('.scan-row button[type=submit]');
    await expect(add).toHaveAccessibleName('Add');
    const ab = (await add.boundingBox())!;
    expect(ab.width).toBeGreaterThanOrEqual(44);
    expect(ab.height).toBeGreaterThanOrEqual(44);
  });

  test('Pay {total} opens the payment panel, whose leading preferred method completes the sale in one tap', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await scanCoke(page);

    const pay = page.getByTestId('payment-open');
    await expect(pay).toBeEnabled();
    await expect(pay).toContainText('£');
    await pay.click();
    const overlay = page.locator('#payment-overlay');
    await expect(overlay).toBeVisible();

    // The quick-pay button's job lives here now: the preferred method is
    // the grid's first button, highlighted across the whole first row.
    const preferred = overlay.getByTestId('pay-default');
    await expect(preferred).toBeVisible();
    await expect(overlay.locator('.pay-grid > .btn').first()).toHaveAttribute('data-testid', 'pay-default');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      preferred.click(),
    ]);
    await expect(page.locator('#basket.receipt-view')).toBeVisible();
    await expect(overlay).toBeHidden();
    assertClean();
  });

  test('Hold parks the sale, the Open orders badge counts it, and the popup resumes it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await clearAllHeldSales(page);
    await page.reload();
    const badge = page.getByTestId('open-orders-badge');
    await expect(badge).toHaveAttribute('data-count', '0');
    await expect(badge).toBeHidden();

    try {
      await scanCoke(page);
      await page.getByTestId('tender-footer-hold').click();
      const modal = page.locator('#hold-modal');
      await expect(modal).toBeVisible();
      await page.locator('#hold-label-input').fill('Badge 2702');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
        modal.locator('button[type=submit]').click(),
      ]);
      await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

      // The badge refreshes itself on held-changed -- no reload.
      await expect(badge).toBeVisible();
      await expect(badge).toHaveText('1');

      await page.getByTestId('parked-orders-open').click();
      const popup = page.locator('#parked-orders-modal');
      await expect(popup).toBeVisible();
      await popup.locator('.parked-order', { hasText: 'Badge 2702' }).click();
      await expect(page.locator('#basket')).toContainText('Coca-Cola');
      await expect(badge).toBeHidden();
    } finally {
      await clearAllHeldSales(page);
    }
    assertClean();
  });

  test('New sale clears the basket', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await scanCoke(page);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      page.getByTestId('kiosk-checkout-start').click(),
    ]);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    await expect(page.getByTestId('payment-open')).toBeDisabled();
    assertClean();
  });

  // A long Pay label (a big total in a long locale) must shrink inside its
  // own box, never wrap the row onto two lines or push an icon off it --
  // the 1024x600 kiosk floor is the tightest width this row gets.
  test('a long Pay label never wraps the row or pushes the icons off it at 1024x600', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const row = page.locator('.tender-default-footer');
    const before = (await row.boundingBox())!;
    await page.getByTestId('payment-open').evaluate((el) => { el.textContent = 'Zahlung mit Karte oder bar 1.234.567,89 €'; });
    const after = (await row.boundingBox())!;
    expect(after.height, 'the row must stay one line').toBeCloseTo(before.height, 0);
    for (const id of ACTION_ROW.slice(1)) {
      const b = (await page.getByTestId(id).boundingBox())!;
      expect(b.x + b.width, `${id} must stay inside the row`).toBeLessThanOrEqual(after.x + after.width + 1);
      expect(b.width, `${id} must keep its 44px`).toBeGreaterThanOrEqual(44);
    }
  });
});
