import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// Farshid's field report: "still don't see the on-screen keyboard". Auto
// hides it on non-touch (this browser) BY DESIGN; forcing On in settings
// must make the real keyboard appear when an input focuses.
test('forcing the OSK on shows a real keyboard', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');
  const osk = page.locator('form[hx-post="/api/settings/osk"] select');
  await osk.selectOption('on');
  await Promise.all([
    page.waitForLoadState('load'),
    osk.locator('..').locator('button[type=submit]').click(),
  ]);

  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');
  await page.getByRole('textbox').first().click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('#osk .osk-key').first()).toBeVisible();

  // And keys actually type into the focused field.
  await page.locator('#osk button[data-k="1"]').click();
  await expect(page.getByRole('textbox').first()).toHaveValue('1');

  // Restore auto so later specs aren't covered by the keyboard (server-side
  // setting shared by the whole suite).
  await page.goto('/settings');
  const osk2 = page.locator('form[hx-post="/api/settings/osk"] select');
  await osk2.selectOption('auto');
  await Promise.all([
    page.waitForLoadState('load'),
    osk2.locator('..').locator('button[type=submit]').click(),
  ]);
  assertClean();
});
