import { test, expect } from './fixtures';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#6179: osk.js's Enter always blurred the field and submitted its
// form, so a multi-line textarea could not take a second line on a touch till
// and the receipt designer reloaded, losing unsaved edits. A textarea that
// opts in with data-osk-enter="newline" now types a line break and the
// keyboard stays open; every other textarea keeps submitting.

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('receipt designer slip text: OSK Enter inserts a line break, keeps the keyboard and the page', async ({ page }, testInfo) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await setOskMode(page, 'on');
  await page.goto('/receipt-designer');
  const header = page.locator('input[name="header1"]');
  await header.fill('Unsaved header');
  const area = page.locator('textarea[name="voucher_slip_text"]');
  await expect(area).toHaveAttribute('data-osk-enter', 'newline');
  await area.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(area).toHaveAttribute('inputmode', 'none');

  await page.locator('#osk button[data-k="a"]').click();
  await page.locator('#osk button[data-k="↵"]').click();
  await page.locator('#osk button[data-k="b"]').click();

  await expect(area).toHaveValue('a\nb');
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page).toHaveURL(/\/receipt-designer$/);
  await expect(header).toHaveValue('Unsaved header');
  // The live preview follows (input event) and shows the sample slip.
  await expect(page.locator('#rd-preview')).toContainText('GV-SAMPLE');
  await page.screenshot({ path: testInfo.outputPath('osk-newline-6179.png'), fullPage: true });
  assertClean();
});
