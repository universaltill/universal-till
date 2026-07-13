import { test, expect } from '../support/fixtures';

test('scan adds seeded item to basket', async ({ page, request }) => {
  const createResponse = await request.post('/api/catalog/item', {
    form: {
      name: 'Test Item',
      price: '1000',
      sku: 'PLU001',
    },
  });
  if (!createResponse.ok()) {
    expect(createResponse.status()).toBe(400);
    const body = await createResponse.text();
    expect(body).toMatch(/items\\.sku|constraint failed/i);
  }

  await page.goto('/');

  const basket = page.locator('#basket');
  await expect(basket).toBeVisible();
  await expect(basket.locator('#basket-lines')).toBeVisible();

  await page.getByLabel('Barcode').fill('PLU001');
  await page.getByLabel('Qty').fill('1');
  await Promise.all([
    page.waitForResponse((response) => response.url().includes('/api/pos/scan') && response.ok()),
    page.getByRole('button', { name: 'Add', exact: true }).click(),
  ]);

  // Compact basket rows show the SKU as plain small text (no parentheses).
  await expect(basket).toContainText('PLU001');
});
