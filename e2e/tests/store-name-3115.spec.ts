import { test, expect } from './fixtures';

// ut-docs#3115: Settings → My shop → Shop name edits store.name after the
// setup wizard. A blank name is refused with a message in the card.

test.describe('shop name (ut-docs#3115)', () => {
  let original = '';

  test.beforeEach(async ({ page }) => {
    // The stored value, placeholder or not, from the All Settings list.
    await page.goto('/settings?lang=en');
    const row = page.locator('#settings-all tr', { has: page.locator('code', { hasText: /^store\.name$/ }) });
    original = await row.locator('input[name=value]').inputValue();
  });

  test.afterEach(async ({ page }) => {
    // Put the till's name back exactly as it was (the raw upsert also
    // accepts a placeholder such as "My Store", which the card refuses).
    if (!original) return;
    const resp = await page.request.post('/api/settings/upsert', { form: { key: 'store.name', value: original } });
    expect(resp.status()).toBeLessThan(300);
  });

  test('renaming the shop sticks across a reload', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-store-name'); // two-pane Settings: deep-link to the section
    const card = page.locator('#settings-store-name');
    const input = card.getByTestId('store-name-input');
    await expect(input).toBeVisible();
    await expect(input).toHaveAttribute('maxlength', '80');
    await expect(input).toHaveAttribute('dir', 'auto');

    await input.fill('Corner Café 3115');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/store-name') && r.status() === 204),
      card.getByTestId('store-name-save').click(),
    ]);

    await page.goto('/settings?lang=en#settings-store-name');
    await expect(page.getByTestId('store-name-input')).toHaveValue('Corner Café 3115');
  });

  test('a blank name is refused with a message', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-store-name');
    const card = page.locator('#settings-store-name');
    await card.getByTestId('store-name-input').fill('   ');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/store-name') && r.status() === 400),
      card.getByTestId('store-name-save').click(),
    ]);
    await expect(card.getByTestId('store-name-msg')).toContainText('Enter your shop’s name');
    // Refusal is shown in the card only — no page-wide error banners.
    await expect(page.locator('#settings-save-error')).toBeHidden();
    await expect(page.locator('#pos-alert')).toBeHidden();
  });
});
