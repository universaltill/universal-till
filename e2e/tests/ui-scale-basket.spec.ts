import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// Farshid's field report, 2026-07-29: on a real high-density small
// touchscreen (10.1" 1920x1200, ~224 PPI), even the then-max 150% scale
// still rendered text too small to read, and separately -- a distinct,
// worse bug -- a scanned item was added to the sale (the total updated)
// but its row was completely invisible in the basket, not merely clipped:
// the basket-scroll flex child's computed height collapsed to literal 0
// once the toggle row + pinned totals outgrew the grid row's allotted
// space at high scale. Guards both: the settings page must offer scale
// options up to the backend's already-validated 2.0 ceiling, and the
// basket item list must never fully disappear at that ceiling.
test.use({ viewport: { width: 1920, height: 1200 } });

test('basket stays visible at the maximum ui_scale on a short viewport', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');
  const scaleSelect = page.locator('form[hx-post="/api/settings/ui-scale"] select');
  await expect(scaleSelect.locator('option[value="2"]')).toHaveCount(1);
  await scaleSelect.selectOption('2');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
    scaleSelect.locator('..').locator('button[type=submit]').click(),
  ]);
  await page.waitForEvent('load');

  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  // #basket is a full hx-swap="outerHTML" -- wait for that exact response
  // before inspecting layout, or a race between the swap and boundingBox()
  // can read a stale/mid-swap node.
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('.basket table tbody tr')).toHaveCount(1);

  // Not just "present in the DOM" -- genuinely rendered with non-zero size,
  // which is exactly what the flex-collapse bug violated.
  const row = page.locator('.basket table tbody tr').first();
  await expect(row).toBeVisible();
  await expect.poll(async () => (await row.boundingBox())?.height ?? 0).toBeGreaterThan(0);
  await expect.poll(async () => (await page.locator('.basket-scroll').boundingBox())?.height ?? 0).toBeGreaterThan(0);

  // Restore default so later specs sharing this server aren't affected.
  await page.goto('/settings');
  const scaleSelect2 = page.locator('form[hx-post="/api/settings/ui-scale"] select');
  await scaleSelect2.selectOption('1');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
    scaleSelect2.locator('..').locator('button[type=submit]').click(),
  ]);
  await page.waitForEvent('load');
  assertClean();
});
