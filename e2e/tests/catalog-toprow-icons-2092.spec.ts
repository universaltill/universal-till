import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2092: the Catalog top action row drops the two rail-duplicate
// buttons (Modifiers, Option sets — already reachable from the /items left
// rail) and the remaining five controls (Add item, Import, Tax codes,
// Export, Barcode backfill) go icon-only, specifically so the row fits one
// line at the kiosk floor (1024x600) without wrapping. These pin real
// geometry (bounding boxes), not element presence, per the categories
// dialog spec's own established pattern.

const TOP_ROW_BUTTON_IDS = [
  '#item-form-add-btn',
  '#catalog-export-btn',
  '#catalog-barcode-backfill-btn',
];

test.describe('catalog top action row icon buttons (ut-docs#2092)', () => {
  test('rail-duplicate buttons are gone; the rest keep a real accessible name', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');

    await expect(page.locator('a[href="/modifiers"]')).toHaveCount(0);
    await expect(page.locator('a[href="/catalog/option-sets"]')).toHaveCount(0);

    for (const id of TOP_ROW_BUTTON_IDS) {
      const el = page.locator(id);
      await expect(el).toBeVisible();
      expect(await el.getAttribute('aria-label'), `${id} aria-label`).toBeTruthy();
      expect(await el.getAttribute('title'), `${id} title`).toBeTruthy();
    }
    const importLink = page.locator('a[href="/import"]');
    const taxCodesLink = page.locator('a[href="/catalog/tax-codes"]');
    for (const link of [importLink, taxCodesLink]) {
      expect(await link.getAttribute('aria-label')).toBeTruthy();
      expect(await link.getAttribute('title')).toBeTruthy();
    }
    assertClean();
  });

  test('the row fits one line at 1024x600 with no wrap', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');

    const addBtnBox = (await page.locator('#item-form-add-btn').boundingBox())!;
    const searchBox = (await page.locator('#catalog-search').boundingBox())!;
    // Same row, single line: the two controls' vertical centers should
    // land within one control's height of each other. Before this card,
    // the row's own comment documented that it no longer fit one line at
    // this width and wrapped instead.
    const addCenterY = addBtnBox.y + addBtnBox.height / 2;
    const searchCenterY = searchBox.y + searchBox.height / 2;
    expect(Math.abs(addCenterY - searchCenterY), 'Add item and the search box should be vertically aligned on one row').toBeLessThan(addBtnBox.height);
    assertClean();
  });

  test('no control runs off-screen or overlaps at phone width (360x800)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/catalog');

    const selectors = [
      ...TOP_ROW_BUTTON_IDS,
      'a[href="/import"]',
      'a[href="/catalog/tax-codes"]',
      '#catalog-search',
    ];
    const boxes: { selector: string; box: { x: number; y: number; width: number; height: number } }[] = [];
    for (const selector of selectors) {
      const box = (await page.locator(selector).boundingBox())!;
      expect(box.x, `${selector} should not start off the left edge`).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width, `${selector} should not run past the 360px viewport`).toBeLessThanOrEqual(360);
      boxes.push({ selector, box });
    }
    // Real overlap check (not just "each box independently fits the
    // viewport") — two controls sharing pixels is exactly the failure mode
    // a flex-wrap row can still produce even when every box individually
    // stays inside 0..360, per list-and-dialog-pattern.md's own "no
    // overlap" geometry standard.
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i].box;
        const b = boxes[j].box;
        const overlaps = a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
        expect(overlaps, `${boxes[i].selector} should not overlap ${boxes[j].selector}`).toBe(false);
      }
    }
    assertClean();
  });

  test('RTL (fa): the row still renders with accessible names and no console error', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog?lang=fa');

    await expect(page.locator('a[href="/modifiers"]')).toHaveCount(0);
    await expect(page.locator('a[href="/catalog/option-sets"]')).toHaveCount(0);
    for (const id of TOP_ROW_BUTTON_IDS) {
      const el = page.locator(id);
      await expect(el).toBeVisible();
      expect(await el.getAttribute('aria-label')).toBeTruthy();
    }
    assertClean();
  });
});
