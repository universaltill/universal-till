import { test, expect, Page } from './fixtures';
import { createTable, deactivateAllTables, drainParkedOrders, watchConsole } from './helpers';

// ut-docs#3630: ut-docs#3622 made a second tap at the Move table toggle's
// position harmless (it no longer resumes the order) but it still did
// nothing: the open <details> relocated to row 2, so the <summary> moved
// from the right of row 1 to the left of row 2. The toggle's hit target
// must stay put while the panel is open, so tapping the same spot again
// CLOSES it.
//
// Only 1024x600 is driven: the sell screen's scan/hold/parked-orders flow
// has no 360px layout yet (no .scan-row -- ut-docs#3060), so the
// max-width:40rem row rules are not reachable through this flow.

const A = 'E2E Toggle Stable A 3630';
const B = 'E2E Toggle Stable B 3630';

test.beforeEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  await deactivateAllTables(page);
  await createTable(page, A);
  await createTable(page, B);
});

test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  await deactivateAllTables(page);
});

async function openPopup(page: Page) {
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/');
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('table-picker-open').click();
  await page.locator('.table-picker-option', { hasText: A }).click();
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-label-input').fill('Toggle stable');
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  return modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Toggle stable' }) });
}

for (const dir of ['ltr', 'rtl']) {
  test(`the Move table toggle stays put when opened, so a second tap closes it (${dir})`, async ({ page }) => {
    const assertClean = watchConsole(page);
    const row = await openPopup(page);
    if (dir === 'rtl') {
      await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    }
    const toggle = row.locator('.parked-move-toggle');
    const details = row.locator('details.parked-move');
    const closed = (await toggle.boundingBox())!;
    const x = closed.x + closed.width / 2;
    const y = closed.y + closed.height / 2;
    const orderClosed = (await row.locator('.parked-order').boundingBox())!;
    const cancelBox = (await row.locator('.parked-order-cancel').boundingBox())!;
    const cx = cancelBox.x + cancelBox.width / 2;
    const cy = cancelBox.y + cancelBox.height / 2;

    await page.mouse.click(x, y);
    await expect(row.locator('.parked-move-options')).toBeVisible();

    const open = (await toggle.boundingBox())!;
    for (const k of ['x', 'y', 'width', 'height'] as const) {
      expect(Math.abs(open[k] - closed[k]), `toggle ${k} moved (closed ${JSON.stringify(closed)}, open ${JSON.stringify(open)})`).toBeLessThanOrEqual(1);
    }
    // The placeholder holds column 2's width: without it the column collapses
    // when <details> leaves it and the order button grows under the summary,
    // hiding its meta text (review finding, ut-docs#3630).
    const orderOpen = (await row.locator('.parked-order').boundingBox())!;
    for (const k of ['x', 'width', 'height'] as const) {
      expect(Math.abs(orderOpen[k] - orderClosed[k]), `order button reflowed (closed ${JSON.stringify(orderClosed)}, open ${JSON.stringify(orderOpen)})`).toBeLessThanOrEqual(1);
    }
    const hit = await page.evaluate(([px, py]) => {
      const el = document.elementFromPoint(px, py);
      return !!el?.closest('summary.parked-move-toggle');
    }, [x, y]);
    expect(hit, 'summary must be the hit target at the original centre').toBe(true);

    // Cancel still wins its own centre while the panel is open.
    const cancelHit = await page.evaluate(([px, py]) => {
      return !!document.elementFromPoint(px, py)?.closest('.parked-order-cancel');
    }, [cx, cy]);
    expect(cancelHit, 'Cancel must stay the hit target at its centre').toBe(true);

    await page.mouse.click(x, y);
    await expect(row.locator('.parked-move-options')).toBeHidden();
    await expect(details).not.toHaveAttribute('open', /.*/);
    await expect(page.locator('#parked-orders-modal')).toBeVisible();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    assertClean();
  });
}
