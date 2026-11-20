import { test, expect } from './fixtures';
import { drainParkedOrders, openPhoneSheet, watchConsole } from './helpers';

// ut-docs#1387: a held order that is all-takeaway shows a "Takeaway" tag in
// the sale screen's Open orders popup and on /open-orders, so a cashier can
// tell at a glance why it has no table / no Move table control. A dine-in
// order (the default) shows none. The server-rendered half is pinned by
// internal/pages/open_orders_page_test.go; this drives it in a real browser.

const TAKEAWAY_LABEL = 'Chip takeaway 1387';
const DINE_IN_LABEL = 'Chip dine-in 1387';

test.beforeEach(async ({ page }) => {
  await drainParkedOrders(page.request);
});

test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
});

async function park(page, label: string, takeaway: boolean) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  if (takeaway) {
    await page.locator('[data-testid="order-type-takeaway"]').click();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveAttribute('aria-checked', 'true');
  }
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

test('a takeaway held order shows a Takeaway tag in the popup and on /open-orders; dine-in does not', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await park(page, TAKEAWAY_LABEL, true);
  await park(page, DINE_IN_LABEL, false);
  await expect(page.getByTestId('open-orders-badge')).toHaveAttribute('data-count', '2');

  // Popup on the sale screen.
  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  const popupTakeaway = modal.locator('li', { has: page.locator('.parked-order', { hasText: TAKEAWAY_LABEL }) });
  const popupDineIn = modal.locator('li', { has: page.locator('.parked-order', { hasText: DINE_IN_LABEL }) });
  await expect(popupTakeaway.locator('.held-chip-takeaway')).toHaveText('Takeaway');
  await expect(popupTakeaway.locator('.held-chip-table')).toHaveCount(0);
  await expect(popupTakeaway.locator('.parked-move-toggle')).toHaveCount(0);
  await expect(popupDineIn.locator('.held-chip-takeaway')).toHaveCount(0);

  // The /open-orders page.
  await page.goto('/open-orders');
  const rowTakeaway = page.locator('tr', { has: page.locator('.open-order-cell-label', { hasText: TAKEAWAY_LABEL }) });
  const rowDineIn = page.locator('tr', { has: page.locator('.open-order-cell-label', { hasText: DINE_IN_LABEL }) });
  await expect(rowTakeaway.locator('.held-chip-takeaway')).toHaveText('Takeaway');
  await expect(rowTakeaway.locator('.held-chip-table')).toHaveCount(0);
  await expect(rowDineIn.locator('.held-chip-takeaway')).toHaveCount(0);
  assertClean();
});

// UX blocker (1387 fix round): at a 360px phone width the chip in the popup
// collapsed to an empty pill because it shared the table chip's shrink rule.
test('the Takeaway chip in the popup is fully readable at 360px', async ({ page }) => {
  await page.goto('/');
  await park(page, TAKEAWAY_LABEL, true);
  await page.setViewportSize({ width: 360, height: 700 });
  await page.goto('/');
  await openPhoneSheet(page);
  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  const chip = modal.locator('.held-chip-takeaway');
  await expect(chip).toHaveText('Takeaway');
  const m = await chip.evaluate((el) => ({ w: el.getBoundingClientRect().width, sw: el.scrollWidth, cw: el.clientWidth }));
  expect(m.sw).toBeLessThanOrEqual(m.cw);
  expect(m.w).toBeGreaterThan(40);
});
