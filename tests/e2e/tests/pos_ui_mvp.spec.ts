import { test, expect } from '../support/fixtures';

test.describe('POS UI MVP Uplift', () => {
  test('kiosk shell renders key cashier flow entrypoints', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expect(page.getByTestId('kiosk-checkout-start')).toBeVisible();
    await expect(page.getByTestId('kiosk-inventory-link')).toBeVisible();
  });

  test('plugin entrypoints are accessible from navigation', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByTestId('nav-help-support')).toBeVisible();
    await page.getByTestId('nav-help-support').click();
    await expect(page.getByTestId('plugin-faq-entry')).toBeVisible();
  });

  test('offline status indicator is present and non-blocking', async ({ page }) => {
    await page.goto('/');

    const status = page.getByTestId('status-indicator');
    await expect(status).toBeVisible();
    await expect(status).toHaveText(/offline|online/i);
  });

  test('accessibility baseline for primary actions', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByRole('button', { name: 'Add', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Complete Sale', exact: true })).toBeVisible();
  });
});
