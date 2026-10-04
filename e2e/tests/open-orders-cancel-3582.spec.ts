import { test, expect } from './fixtures';
import { drainParkedOrders, watchConsole } from './helpers';

// ut-docs#3582 (review): the explicit Cancel order action, driven in a real
// browser on both surfaces. The Go handler tests cover the gate, the audit
// row, the claim and the fiscal event; what only a browser can show is the
// htmx wiring around them -- the native hx-confirm (catalog delete's
// pattern), the HX-Retarget from the small hint element back onto the popup
// body, the held-changed badge refresh, and the /open-orders page's
// HX-Redirect landing on its one-shot banner. The default (auth-off) till
// holds void_comp_waste, so no PIN prompt is expected here; the elevation
// path is covered at the handler level (held_cancel_test.go) and the prompt
// itself is the shared modal every other gated action already proves.
//
// Each test drains afterwards so the rows it parks never reach another file.
test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
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

test('Cancel order on the popup asks first, then drops the row and says so', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await drainParkedOrders(page.request);
  await page.reload();
  await parkASale(page, 'Cancel me');
  await parkASale(page, 'Keep me');

  await page.getByTestId('parked-orders-open').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('.parked-order')).toHaveCount(2);

  const cancel = modal.locator('.parked-order-cancel[aria-label="Cancel order — Cancel me"]');
  await expect(cancel).toBeVisible();
  // Touch floor on every surface (ut-docs#161): never below 46px.
  const box = (await cancel.boundingBox())!;
  expect(box.width).toBeGreaterThanOrEqual(46);
  expect(box.height).toBeGreaterThanOrEqual(46);

  // Dismissing the confirm cancels nothing.
  page.once('dialog', async (d) => {
    expect(d.type()).toBe('confirm');
    expect(d.message()).toContain('Cancel me');
    expect(d.message()).toContain('cannot be undone');
    await d.dismiss();
  });
  await cancel.click();
  await expect(modal.locator('.parked-order')).toHaveCount(2);

  // Accepting it removes exactly that order, the popup re-renders in place
  // with the toast (not the hint), and the other order is untouched.
  page.once('dialog', (d) => d.accept());
  await cancel.click();
  await expect(modal.locator('.parked-order', { hasText: 'Cancel me' })).toHaveCount(0);
  await expect(modal.locator('.parked-order', { hasText: 'Keep me' })).toHaveCount(1);
  await expect(modal.locator('#parked-orders-toast')).toContainText('Order cancelled');
  await expect(modal.locator('#parked-orders-hint')).toBeEmpty();
  // The Open orders badge follows (held-changed).
  await expect(page.getByTestId('open-orders-badge')).toHaveAttribute('data-count', '1');
  assertClean();
});

test('Cancel order on /open-orders redirects back to the tab with a one-shot banner', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await drainParkedOrders(page.request);
  await page.reload();
  await parkASale(page, 'Page cancel');

  await page.goto('/open-orders?tab=hold');
  const row = page.locator('#open-orders-table tbody tr[data-held-id]');
  await expect(row).toHaveCount(1);
  const cancel = page.locator('#open-orders-table tbody .open-order-cancel');
  await expect(cancel).toHaveAttribute('aria-label', 'Cancel order — Page cancel');

  page.once('dialog', (d) => d.accept());
  await cancel.click();

  await expect(page).toHaveURL(/\/open-orders\?tab=hold&msg=open_orders\.cancel\.done/);
  await expect(page.getByTestId('open-orders-notice')).toContainText('Order cancelled');
  // Cancelling the last order leaves the tab's own empty state, not a bare table.
  await expect(page.getByTestId('open-orders-empty')).toBeVisible();
  await expect(page.locator('#open-orders-table')).toHaveCount(0);
  assertClean();
});
