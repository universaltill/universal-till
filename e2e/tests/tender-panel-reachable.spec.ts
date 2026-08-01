import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#161's independent review found TWO real regressions on the way to
// making the sale screen viewport-responsive, both in the tender panel
// (Cash/Card/Gift Card/Hold Sale/New Customer):
//
// 1. A width-only fluid root font-size inflated every rem on a wide-but-
//    short screen without regard to the vertical budget, so the fixed-
//    height, no-page-scroll tender panel's content outgrew its box and
//    `overflow: hidden` clipped the payment buttons off screen entirely.
// 2. The first fix (making the panel `overflow-y: auto` instead) exposed a
//    SEPARATE, worse failure: `.tab-panel` (`flex: 1; min-height: 0`)
//    collapsed to a real, hit-testable 0 clientHeight once its ancestor
//    became scrollable -- the buttons weren't clipped, they rendered
//    nowhere. Bounding-box/isVisible/scrollIntoViewIfNeeded assertions all
//    return true for an element inside a zero-height container, so a naive
//    geometry-based regression test would have passed against the broken
//    build -- this spec hit-tests for real (elementFromPoint + an actual
//    click that completes a sale) instead of trusting geometry.
//
// The actual fix: .tab-panel gets the same `min-height: 6rem` floor
// `.basket-scroll` already uses for the identical collapse class (app.css).
// The review also found this exact collapse pre-existed on main at plain
// 1024x600 default scale (an AC resolution, no manual UI-scale involved) --
// so this guards a real, previously-shipped bug, not just the new fluid
// sizing.
test.describe('tender panel stays reachable under viewport + UI-scale pressure', () => {
  test('payment buttons are real hit-test targets at 1024x600, default scale', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const tabPanelHeight = await page.evaluate(() => (document.querySelector('.tab-panel') as HTMLElement)?.clientHeight ?? -1);
    expect(tabPanelHeight, '.tab-panel must never collapse to ~0').toBeGreaterThan(40);

    const cashBtn = page.locator('.tab-panel .btn', { hasText: 'Cash' }).first();
    await cashBtn.scrollIntoViewIfNeeded();
    const hit = await cashBtn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'Cash must be the real hit-test target, not occluded by a collapsed ancestor').toBe(true);

    // A real click completing a real sale is the strongest proof: it
    // fails if the button is present-but-unclickable in any way a
    // geometry check can't see.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      cashBtn.click(), // no force: must be a genuinely landable click
    ]);
    await expect(page.locator('#basket.receipt-view')).toBeVisible();
    assertClean();
  });

  test('payment buttons are real hit-test targets on a wide-short screen at a high manual UI scale', async ({ page }) => {
    const assertClean = watchConsole(page);
    // Reproduces the independent review's worst-case class: a wide,
    // short viewport combined with the existing manual UI-scale setting
    // (up to 2.0x, ADR-untouched, pre-existing feature) stacking on top
    // of the automatic viewport fit.
    await page.setViewportSize({ width: 1920, height: 800 });
    await page.goto('/settings');
    const scaleSelect = page.locator('form[hx-post="/api/settings/ui-scale"] select');
    await scaleSelect.selectOption('2');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
      scaleSelect.locator('..').locator('button[type=submit]').click(),
    ]);
    await page.waitForEvent('load');

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const tabPanelHeight = await page.evaluate(() => (document.querySelector('.tab-panel') as HTMLElement)?.clientHeight ?? -1);
    expect(tabPanelHeight, '.tab-panel must never collapse to ~0 at a high manual scale').toBeGreaterThan(40);

    const cashBtn = page.locator('.tab-panel .btn', { hasText: 'Cash' }).first();
    await cashBtn.scrollIntoViewIfNeeded();
    const hit = await cashBtn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'Cash must be the real hit-test target at a high manual UI scale').toBe(true);

    const newCustomerBtn = page.locator('.tender-footer .btn', { hasText: 'New Customer' }).first();
    await newCustomerBtn.scrollIntoViewIfNeeded();
    const footerHit = await newCustomerBtn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(footerHit, 'New Customer (tender-footer, outside .tab-panel) must also be reachable').toBe(true);

    // Restore default scale so later specs sharing this server aren't affected.
    await page.goto('/settings');
    const restore = page.locator('form[hx-post="/api/settings/ui-scale"] select');
    await restore.selectOption('1');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
      restore.locator('..').locator('button[type=submit]').click(),
    ]);
    await page.waitForEvent('load');
    assertClean();
  });
});
