import { test, expect } from './fixtures';

// ut-docs#3086: the ☰ Menu's language row lists only the languages the shop
// chose in Settings → Languages shown to staff (default: the shop's language
// plus English), and hides itself when just one is chosen.

test.describe('staff languages (ut-docs#3086)', () => {
  test.afterEach(async ({ page }) => {
    // Back to the unset default (English shop → English only).
    // Fails loudly if a spec ever changes this worker's default language.
    const resp = await page.request.post('/api/settings/staff-languages', { form: { staff_locales: 'en' } });
    expect(resp.status()).toBe(204);
  });

  test('the Menu shows only the languages ticked in Settings', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-staff-languages'); // two-pane Settings (ut-docs#1960): deep-link to the section
    const card = page.locator('#settings-staff-languages');
    // Every installed language is offered; the default is fixed on.
    await expect(card.getByTestId('staff-lang-en')).toBeChecked();
    await expect(card.getByTestId('staff-lang-en')).toBeDisabled();
    await expect(card.getByTestId('staff-lang-fa')).not.toBeChecked();
    await card.getByTestId('staff-lang-tr').check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/staff-languages') && r.status() === 204),
      card.getByRole('button').click(),
    ]);

    await page.goto('/menu?lang=en');
    const row = page.getByTestId('menu-lang');
    await expect(row).toBeVisible();
    await expect(row.locator('a.menu-lang-btn')).toHaveCount(2);
    await expect(row.getByText('English')).toBeVisible();
    await expect(row.getByText('Türkçe')).toBeVisible();
    await expect(row.getByText('فارسی')).toHaveCount(0);
  });

  test('with one language chosen the Menu has no language row', async ({ page }) => {
    const resp = await page.request.post('/api/settings/staff-languages', { form: { staff_locales: 'en' } });
    expect(resp.status()).toBe(204);
    await page.goto('/menu?lang=en');
    await expect(page.getByTestId('menu-lang')).toHaveCount(0);
  });
});
