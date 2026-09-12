import { test, expect } from './fixtures';
import { drainParkedOrders, watchConsole } from './helpers';

// ut-docs#2137: a parked order was unreachable on the pilot tablet. The sale
// screen's On hold strip is clipped off-screen at 1280x800 (ut-docs#2128) and
// /open-orders was read-only, telling the cashier to "tap it on the On hold
// strip" -- a control that device does not show. Reported by the product
// owner as "the open orders are not tapable", with three real orders stranded,
// one open 221 minutes.
//
// The fix is a button beside Card that opens a popup of parked orders, each
// tappable to resume. It depends on no viewport budget, which is the whole
// point: it works at the resolution the strip's CSS tuning never covered.
test.afterEach(async ({ page }) => {
  await page.request.post('/api/pos/reset').catch(() => {});
});

async function parkASale(page, label: string) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

test('a parked order can be picked back up from the popup beside Card', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await parkASale(page, 'Table 9');

  // The trigger is in the quick-pay row, next to Card -- the product owner's
  // own placement, and the reason this is reachable at all on a 1280x800
  // tablet where the strip is not.
  const trigger = page.locator('.tender-quickpay [data-testid="parked-orders-open"]');
  await expect(trigger).toBeVisible();
  await trigger.click();

  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  await expect(modal).toContainText('Table 9');

  // Same non-modal requirement as #hold-modal: showModal() would make the
  // rest of the document inert and the kiosk's on-screen keyboard dead.
  expect(await page.evaluate(() => document.body.inert)).toBe(false);

  await modal.locator('.parked-order', { hasText: 'Table 9' }).click();

  // Resuming loads the basket the cashier is looking at, and the popup gets
  // out of the way on its own -- no second tap to dismiss it.
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(modal).toBeHidden();
  assertClean();
});

test('the popup says so when nothing is parked, rather than opening empty', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await drainParkedOrders(page);
  await page.reload();

  await page.locator('.tender-quickpay [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('[data-testid="parked-orders-empty"]')).toBeVisible();
  await expect(modal.locator('.parked-order')).toHaveCount(0);
  assertClean();
});

// A refused resume must get out of the way (ut-docs#2137 review). The dialog
// sits over the right-hand side of the toast, so leaving it open lets the
// cashier read "Finish or hold the current sale first" but not dismiss it --
// and the refusal is telling them to act on the sale screen the popup is
// covering.
test('a resume refused because the basket is busy closes the popup and says why', async ({ page }) => {
  await page.goto('/');
  await parkASale(page, 'Table 5');

  // A new sale is now in progress, so the parked one cannot be resumed.
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-quickpay [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  await modal.locator('.parked-order', { hasText: 'Table 5' }).click();

  await expect(modal).toBeHidden();
  await expect(page.locator('#toast-message')).toContainText('Finish or hold the current sale first');
  // The order is untouched and still offered.
  await page.locator('.tender-quickpay [data-testid="parked-orders-open"]').click();
  await expect(modal.locator('.parked-order', { hasText: 'Table 5' })).toBeVisible();
});

// Regression guard for a measured CSS bug this popup shipped with in review:
// at 38% flex-basis the trigger is 158px at 1024px wide, which fits the
// English "Open orders" and NOT the German "Offene Vorgänge". The label
// wrapped, the quick-pay row went 51px -> 66.6px, and the bottom of the Card
// button was pushed into the tender pane's scroll -- on the 1024x600 kiosk
// the ut-docs#1336 height budget exists to protect. English alone would never
// have caught it, so this drives the label directly rather than trusting a
// locale to be long enough.
test('a long label does not make the quick-pay row taller', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/');

  const row = page.locator('.tender-quickpay');
  const before = (await row.boundingBox())!.height;

  await page.locator('.tender-quickpay [data-testid="parked-orders-open"]')
    .evaluate((el) => { el.textContent = 'Offene Vorgänge'; });

  const after = (await row.boundingBox())!.height;
  expect(after, 'the trigger\'s label must not wrap the quick-pay row onto two lines')
    .toBeCloseTo(before, 0);
});

// The whole reason this popup exists: it must work at the resolution where
// the strip does not. 1280x800 is the pilot tablet.
test('the trigger and the popup work at the pilot tablet resolution', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await parkASale(page, 'Table 7');

  const trigger = page.locator('.tender-quickpay [data-testid="parked-orders-open"]');
  await expect(trigger).toBeInViewport();

  await trigger.click();
  const entry = page.locator('#parked-orders-modal .parked-order', { hasText: 'Table 7' });
  await expect(entry).toBeInViewport();
  // A finger, not a mouse: the product's documented touch-target floor.
  const box = await entry.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(46);
});
