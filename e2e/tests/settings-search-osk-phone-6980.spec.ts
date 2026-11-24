import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#6980: on the phone the Settings landing-grid search took ONE letter:
// the handler hands the query to the sidebar search and focuses it, but the
// on-screen keyboard kept typing into the (now hidden, emptied) grid field, so
// every key re-ran the hand-off and replaced the previous letter.

const key = (page: Page, ch: string) => page.locator(`#osk button[data-k="${ch}"]`);

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('typing "tap" with the OSK into the Settings grid search keeps every letter', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/settings');
  await page.locator('#settings-home-q').click();
  await expect(page.locator('#osk')).toBeVisible();
  for (const ch of 'tap') await key(page, ch).click();
  await expect(page.locator('#settings-q')).toHaveValue('tap');
  await expect(page.locator('#settings-home-q')).toHaveValue('');
  assertClean();
});

test('typing with the OSK into the in-category Settings search keeps every letter', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/settings#settings-advanced');
  const q = page.locator('#settings-q');
  await q.click();
  await expect(page.locator('#osk')).toBeVisible();
  for (const ch of 'tap') await key(page, ch).click();
  await expect(q).toHaveValue('tap');
  assertClean();
});
