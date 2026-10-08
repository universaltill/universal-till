import { test, expect } from './fixtures';
import { setOrderTypePromptMode } from './helpers';

// ut-docs#3632: a fourth dine-in/takeaway prompt mode, Off, for shops that
// don't sell food or drink to eat in (off-licence, barber, retail). With it
// the sale screen shows no toggle and never opens the intercept dialog, and
// the order-type API refuses takeaway. The server-rendered half is pinned by
// internal/pages/order_type_off_test.go; this file proves the client-side
// half (app.js never prompts) in a real browser.
//
// The setting is server-wide on this till (fixtures.ts's "one live till"
// rule), so afterEach restores 'top' even when a test fails.

test.describe('ut-docs#3632 dine-in/takeaway Off', () => {
  test.afterEach(async ({ page }) => {
    await setOrderTypePromptMode(page, 'top');
    await page.request.post('/api/pos/reset').catch(() => {});
  });

  test('Off: no toggle, items add and Pay opens with no prompt, takeaway refused', async ({ page }) => {
    await setOrderTypePromptMode(page, 'off');
    await page.goto('/');

    await expect(page.locator('body')).toHaveAttribute('data-order-type-prompt-mode', 'off');
    await expect(page.locator('[data-testid="order-type-dine-in"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveCount(0);
    await expect(page.locator('#order-type-prompt-modal')).toHaveCount(0);

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    const resp = await page.request.post('/api/pos/order-type', { form: { order_type: 'takeaway' } });
    expect(resp.status()).toBe(409);
    const body = await resp.json();
    expect(body.error.code).toBe('order_type_off');
  });

  test('Settings offers Off and it survives a reload', async ({ page }) => {
    await setOrderTypePromptMode(page, 'off');
    await page.goto('/settings#settings-order-type-prompt');
    const select = page.locator('form[hx-post="/api/settings/order-type-prompt"] select');
    await expect(select).toHaveValue('off');
  });
});
