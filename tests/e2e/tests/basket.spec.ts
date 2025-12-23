import { test, expect } from '../support/fixtures';

test('scan adds seeded item to basket', async ({ page }) => {
  await page.goto('/');

  const basket = page.locator('#basket');
  await expect(basket).toBeVisible();
  await expect(basket.getByText('No items')).toBeVisible();

  await page.getByLabel('Barcode').fill('PLU001');
  await page.getByLabel('Qty').fill('1');
  await page.getByRole('button', { name: 'Add' }).click();

  await expect(basket.getByText('Test Item (PLU001)')).toBeVisible();
});
