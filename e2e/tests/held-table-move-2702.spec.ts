import { test, expect } from './fixtures';
import { createTable, deactivateAllTables, drainParkedOrders, watchConsole } from './helpers';

// ut-docs#2702 review: the compact tender panel removed the held-sales
// strip, which was the only UI that could move a parked order to another
// table (POST /api/pos/held/table -- ut-docs#820 free-table validation,
// #1704 claim hand-over and occupied toast). The Open orders popup's rows
// now carry that control. These drive it for real: park on table A, move
// it to B from the popup, and a stale offer of an occupied table is refused
// with the occupied toast rather than silently doing nothing.

const A = 'E2E Move A 2702';
const B = 'E2E Move B 2702';
const C = 'E2E Move C 2702';

test.beforeEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  await deactivateAllTables(page);
  for (const label of [A, B, C]) await createTable(page, label);
});

test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
  await deactivateAllTables(page);
});

async function parkOnTable(page, table: string, label: string) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('table-picker-open').click();
  await page.locator('.table-picker-option', { hasText: table }).click();
  await expect(page.getByTestId('table-picker-open')).toContainText(table);
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

async function openPopup(page) {
  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  return modal;
}

test('a parked order moves to another table from the Open orders popup', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await parkOnTable(page, A, 'Move me');
  const badge = page.getByTestId('open-orders-badge');
  await expect(badge).toHaveAttribute('data-count', '1');

  const modal = await openPopup(page);
  const row = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Move me' }) });
  await expect(row.locator('.held-chip-table')).toHaveText(A);

  const toggle = row.locator('.parked-move-toggle');
  // Accessible name carries the order, so rows are distinguishable; the
  // target meets the 44px touch floor.
  await expect(toggle).toHaveAccessibleName(/Move table.*Move me/);
  const box = await toggle.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);

  // Keyboard path: focus + Enter opens the native <details>.
  await toggle.focus();
  await page.keyboard.press('Enter');
  const optB = row.locator('.parked-move-option', { hasText: B });
  await expect(optB).toBeVisible();
  // Own table is never offered.
  await expect(row.locator('.parked-move-option', { hasText: A })).toHaveCount(0);
  expect((await optB.boundingBox())!.height).toBeGreaterThanOrEqual(44);

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/held/table') && r.ok()),
    optB.click(),
  ]);

  // Popup stays open and re-renders with the order on B; A is now offerable.
  await expect(modal).toBeVisible();
  const movedRow = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Move me' }) });
  await expect(movedRow.locator('.held-chip-table')).toHaveText(B);
  await movedRow.locator('.parked-move-toggle').click();
  await expect(movedRow.locator('.parked-move-option', { hasText: A })).toBeVisible();
  await expect(movedRow.locator('.parked-move-option', { hasText: B })).toHaveCount(0);
  // held-changed re-fetched the badge; still one parked order.
  await expect(badge).toHaveAttribute('data-count', '1');
  assertClean();
});

test('moving onto a table taken since the popup opened shows the occupied toast', async ({ page }) => {
  await page.goto('/');
  await parkOnTable(page, A, 'Stale offer');

  const modal = await openPopup(page);
  const row = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Stale offer' }) });
  await row.locator('.parked-move-toggle').click();
  const optB = row.locator('.parked-move-option', { hasText: B });
  await expect(optB).toBeVisible();
  const tableB = await optB.getAttribute('data-table-id');

  // Meanwhile (another cashier, another till) a second order is parked on B.
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  await page.request.post('/api/pos/table', { form: { table_id: tableB! } });
  await page.request.post('/api/pos/hold', { form: { label: 'Took B' } });

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/held/table')),
    optB.click(),
  ]);
  await expect(modal.locator('.pos-notice.error')).toContainText('That table is already occupied');
  // The order stayed on A.
  await expect(
    modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Stale offer' }) }).locator('.held-chip-table'),
  ).toHaveText(A);
});

// #2702 re-review F1/F2: a refused move's notice lives only inside the popup
// (its own id) and is cleared when the popup closes, so the sale screen's
// #toast-message checks never see it -- a following cash sale still closes
// the payment overlay. After a move, keyboard focus returns to that order's
// Move table control instead of dropping to <body>.
test('a refused move leaves no stale toast for the next sale; focus returns after a move', async ({ page }) => {
  await page.goto('/');
  await parkOnTable(page, A, 'Refused');

  let modal = await openPopup(page);
  let row = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Refused' }) });
  await row.locator('.parked-move-toggle').click();
  const optB = row.locator('.parked-move-option', { hasText: B });
  const tableB = await optB.getAttribute('data-table-id');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  await page.request.post('/api/pos/table', { form: { table_id: tableB! } });
  await page.request.post('/api/pos/hold', { form: { label: 'Took B' } });
  await Promise.all([page.waitForResponse((r) => r.url().includes('/api/pos/held/table')), optB.click()]);
  await expect(modal.locator('#parked-orders-toast')).toContainText('That table is already occupied');
  await expect(page.locator('#toast-message.error')).toHaveCount(0);

  await modal.getByRole('button', { name: 'Close' }).click();
  await expect(modal).toBeHidden();
  await expect(page.locator('#parked-orders-body')).toBeEmpty();

  // A normal cash sale afterwards: the payment overlay must close.
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.payment-trigger').click();
  const overlay = page.locator('#payment-overlay');
  await expect(overlay).toBeVisible();
  await overlay.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(overlay).toBeHidden();

  // Focus after a successful move lands on that order's Move table control.
  modal = await openPopup(page);
  row = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Refused' }) });
  await row.locator('.parked-move-toggle').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/held/table') && r.ok()),
    row.locator('.parked-move-option', { hasText: C }).click(),
  ]);
  const moved = modal.locator('li', { has: page.locator('.parked-order', { hasText: 'Refused' }) });
  await expect(moved.locator('.held-chip-table')).toHaveText(C);
  await expect(moved.locator('.parked-move-toggle')).toBeFocused();
});
