import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The one flow that must never break: scan → basket → cash tender → reset.
test('a cash sale completes end to end', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  // Scan a seeded demo barcode (Coca-Cola Can 330ml, £1.20 — the default
  // config is tax-INCLUSIVE (UT_TAX_INCLUSIVE defaults to true), so the
  // displayed price already includes VAT; there's no separate add-on).
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.20');

  // Cash pays exactly; the completed sale shows the receipt view.
  // ut-docs#1252: Cash/Card now live inside the #payment-overlay dialog,
  // opened by the .payment-trigger button, instead of always on screen.
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await expect(page.locator('#basket')).toContainText('1.20');
  assertClean();
});
