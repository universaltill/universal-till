import { test, expect } from './fixtures';
import { createTable, deactivateAllTables, drainParkedOrders, watchConsole } from './helpers';

// ut-docs#3622: on the kiosk floor (1024x600), tapping a parked order's Move
// table toggle opened a <details> panel that (pre-ut-docs#3582) widened the
// order's own row underneath it via flex-wrap, so a second tap at the SAME
// screen position -- meant to close the panel -- could land on the shifted
// order button instead and resume it straight into the basket. ut-docs#3582
// reworked the row to a fixed three-column grid (app.css), which turned out
// to have the SAME failure mode via a different mechanism: with the open
// panel's <details> vacating column 2's row-1 track, that per-<li> grid's
// auto-sized column 2 can collapse, and the order's own minmax(0, 1fr)
// column grows to fill the freed space -- widening into the old toggle
// position. Fixed by making the order's own resume target inert
// (pointer-events: none) for as long as ITS row's panel is open, so a tap
// anywhere it may have shifted to can never resume it.
//
// Only the kiosk floor is driven here: the sell screen's scan/hold/
// parked-orders flow has no 360px phone layout yet (no .scan-row, a
// different nav at that width -- tracked separately as ut-docs#3060, open),
// so there is nothing of this flow to exercise there. The fix itself carries
// no media query, so it is width-independent regardless.

const A = 'E2E Tap Shift A 3622';
const B = 'E2E Tap Shift B 3622';

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

test('a second tap at the Move table toggle\'s screen position never resumes the order underneath', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/');

  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('table-picker-open').click();
  await page.locator('.table-picker-option', { hasText: A }).click();
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-label-input').fill('Tap shift');
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  const row = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Tap shift' }) });
  const toggle = row.locator('.parked-move-toggle');
  const box = (await toggle.boundingBox())!;
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;

  await page.mouse.click(x, y);
  await expect(row.locator('.parked-move-options')).toBeVisible();

  // Deterministic proof the fix is actually what stands in the way here,
  // not a race the resume request happens to lose: whatever now sits at
  // that exact point must not be (or be inside) the order's own resume
  // target, and that target must be pointer-inert.
  await expect(async () => {
    const landsOutsideOrder = await page.evaluate(
      ([px, py]) => !document.elementFromPoint(px, py)?.closest('.parked-order, .parked-order-form'),
      [x, y],
    );
    expect(landsOutsideOrder).toBe(true);
  }).toPass();
  await expect(row.locator('.parked-order')).toHaveCSS('pointer-events', 'none');

  // Second tap at the EXACT same screen position: must never resume the
  // order into the basket, whatever it lands on.
  await page.mouse.click(x, y);
  await expect(modal).toBeVisible();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
  assertClean();
});
