import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The one flow that must never break: scan → basket → cash tender → reset.
test('a cash sale completes end to end', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  // Scan a seeded demo barcode (Coca-Cola Can 330ml, £1.20 + 20% VAT = £1.44).
  await page.getByRole('textbox').first().fill('5000000000011');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.44');

  // Cash pays exactly; the completed sale shows the receipt view.
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await expect(page.locator('#basket')).toContainText('1.44');
  assertClean();
});
