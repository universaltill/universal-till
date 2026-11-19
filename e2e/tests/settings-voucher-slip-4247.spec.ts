import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4247: the "Print each gift voucher on its own slip" checkbox on
// the Receipt printer card is off by default and round-trips through Save.
// The printed output itself is covered by Go tests (no printer in e2e).
test.describe('gift voucher slip setting (ut-docs#4247)', () => {
  test('checkbox is off by default and round-trips through Save printer', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/settings#settings-printer');
    const box = page.getByTestId('printer-voucher-slip');
    await expect(box).toBeVisible();
    await expect(box).not.toBeChecked();

    await box.check();
    await page.locator('#settings-printer').getByRole('button', { name: 'Save printer' }).click();
    await page.reload();
    await expect(page.getByTestId('printer-voucher-slip')).toBeChecked();

    // Restore the default so other specs see an untouched till.
    await page.getByTestId('printer-voucher-slip').uncheck();
    await page.locator('#settings-printer').getByRole('button', { name: 'Save printer' }).click();
    await page.reload();
    await expect(page.getByTestId('printer-voucher-slip')).not.toBeChecked();
    assertClean();
  });
});
