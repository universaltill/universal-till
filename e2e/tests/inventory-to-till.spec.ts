import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The flow Farshid asked for directly: receive stock on the Inventory
// page, then go sell that exact item at the till, then confirm the stock
// level actually dropped — end to end, through the real UI, not a mocked
// API call. Uses Pepsi Can 330ml (itm002, £1.15, barcode 5000000000028) —
// deliberately NOT the Coca-Cola item sale.spec.ts/rtl.spec.ts already
// scan, so stock-level assertions here aren't perturbed by another spec's
// sale landing between the "receive" and "sell" steps.
test('receive stock on Inventory, then sell that item at the till', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.goto('/inventory');
  await expect(page.locator('body')).toContainText('Receive / Adjust Stock');

  // Read the CURRENT quantity first rather than assuming a fixed starting
  // point — itm002 may already have a stock row (or none at all) left
  // over from a previous local run of this same server.
  const pepsiRow = page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' });
  const startingQty = (await pepsiRow.count()) > 0
    ? parseFloat((await pepsiRow.locator('td').nth(3).innerText()).trim())
    : 0;

  // Receive 10 units at the Main Store.
  await page.locator('#stock-item-search').fill('Pepsi Can 330ml (SKU-0002)');
  await expect(page.locator('#stock-item-id')).not.toHaveValue('');
  await page.locator('#stock-location').selectOption({ label: 'Main Store' });
  await page.locator('#stock-form input[name=quantity]').fill('10');
  await page.locator('#stock-form input[name=reason]').fill('e2e: initial delivery');
  await page.locator('#stock-form button[type=submit]').click();
  await expect(page.locator('#result')).toContainText('Stock movement created');

  // The stock table refreshes itself (HX-Trigger: stock-updated) without a
  // page reload — confirm the new total, not just the success message.
  const expectedAfterReceive = startingQty + 10;
  await expect(page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' }).locator('td').nth(3))
    .toHaveText(String(expectedAfterReceive));

  // Now go sell one at the till.
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000028');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Pepsi');
  await expect(page.locator('.basket .total')).toContainText('1.15');
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();

  // Back on Inventory, the sale must have decremented stock by exactly 1.
  await page.goto('/inventory');
  await expect(page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' }).locator('td').nth(3))
    .toHaveText(String(expectedAfterReceive - 1));

  assertClean();
});
