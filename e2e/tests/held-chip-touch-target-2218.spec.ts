import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout, clearAllHeldSales } from './helpers';

// ut-docs#2218: `.held-chip` (app.css) had `min-height: 0`, overriding
// `.btn`'s 3rem/48px base down to a measured ~26-27px hit target -- well
// under this codebase's 44px touch-target floor (`.modifier-option`,
// `.catalog-detail-title .thumb`). Fix: raise `.held-chip` to a real
// `min-height: 44px`, unchanged padding/font-size, so the chip keeps its
// compact look. Unlike ut-docs#1340 (basket qty/disc, rejected: a fixed
// ">=4 rows visible" AC), `.held-chip` lives in `.held-strip` (flex-wrap)
// inside `.tender-scroll` (free-scrolling, no row-count AC), so nothing
// depends on this chip staying small.
//
// Real hit-testing (bounding rect height), not a computed-style read of
// `min-height` alone -- the same live-measurement standard ut-docs#2128's
// review used -- at the two supported floors: the 1280x800 pilot device
// and the 1024x600 kiosk floor.
test.describe('held-chip touch target (ut-docs#2218)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await clearAllHeldSales(page);
  });

  for (const viewport of [
    { width: 1280, height: 800 },
    { width: 1024, height: 600 },
  ]) {
    test(`held chip meets the 44px touch-target floor at ${viewport.width}x${viewport.height}`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(viewport);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await clearAllHeldSales(page);

      await page.locator('input[name="code"]').first().fill('5000000000012');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
        page.locator('.scan-row button[type=submit]').click(),
      ]);
      await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
      const modal = page.locator('#hold-modal');
      await expect(modal).toBeVisible();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
        modal.locator('button[type=submit]').click(),
      ]);
      await expect(modal).toBeHidden();
      await waitForStableLayout(page, '.held-chip');

      const chip = page.locator('.held-chip').first();
      await expect(chip).toBeVisible();
      const height = await chip.evaluate((el) => el.getBoundingClientRect().height);
      expect(height, `.held-chip rendered height at ${viewport.width}x${viewport.height}`).toBeGreaterThanOrEqual(44);

      assertClean();
    });
  }
});
