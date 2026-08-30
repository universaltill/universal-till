import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// Persian: the document flips to RTL and the sale flow still works.
test('Farsi locale renders RTL and can sell', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/?lang=fa');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  // Complete the sale — the basket is server-side and shared by the suite.
  // ut-docs#1252: Cash/Card now live inside the #payment-overlay dialog.
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn').first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  assertClean();
});
