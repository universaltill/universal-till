import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3132: /users/permissions is a wide action × role grid. At tablet
// and phone width it must scroll sideways inside its own card (never the
// page), with the action column pinned at the card's inline-start edge so
// a checkbox is never read against the wrong action — in LTR and RTL.
// Measured, not eyeballed: page scrollWidth vs clientWidth, and the pinned
// column's box before/after scrolling the grid to its far end.

const CASES = [
  { width: 1024, height: 600, lang: 'en', mustScroll: false },
  { width: 360, height: 740, lang: 'en', mustScroll: true },
  { width: 360, height: 740, lang: 'fa', mustScroll: true },
];

test.describe('permissions matrix layout (ut-docs#3132)', () => {
  for (const c of CASES) {
    test(`/users/permissions at ${c.width}x${c.height} (${c.lang})`, async ({ page }) => {
      await page.setViewportSize({ width: c.width, height: c.height });
      const assertClean = watchConsole(page);
      await page.goto(`/users/permissions?lang=${c.lang}`);
      await expect(page.locator('th[scope="rowgroup"]')).toHaveCount(8);
      await expect(page.locator('.perm-desc').first()).toBeVisible();

      const page_ = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(page_.scrollWidth, 'page must not scroll horizontally').toBeLessThanOrEqual(page_.clientWidth);

      const scroller = page.locator('.perm-scroll');
      const canScroll = await scroller.evaluate((el) => el.scrollWidth > el.clientWidth + 1);
      if (c.mustScroll) expect(canScroll, 'grid should need sideways scroll at this width').toBe(true);
      if (!canScroll) {
        assertClean();
        return;
      }

      const pinned = page.locator('tbody th.perm-action').first();
      const roleCell = page.locator('tbody tr:has(th.perm-action) td.perm-cell').last();
      const before = await pinned.boundingBox();
      const cellBefore = await roleCell.boundingBox();
      await scroller.evaluate((el) => {
        const rtl = getComputedStyle(el).direction === 'rtl';
        el.scrollLeft = rtl ? -el.scrollWidth : el.scrollWidth;
      });
      const after = await pinned.boundingBox();
      const cellAfter = await roleCell.boundingBox();
      expect(before && after && cellBefore && cellAfter).toBeTruthy();
      // The role columns moved…
      expect(Math.abs(cellAfter!.x - cellBefore!.x)).toBeGreaterThan(10);
      // …the action column did not: still at the card's inline-start edge.
      expect(Math.abs(after!.x - before!.x)).toBeLessThanOrEqual(1);
      const box = await scroller.boundingBox();
      if (c.lang === 'fa') {
        expect(Math.abs(after!.x + after!.width - (box!.x + box!.width))).toBeLessThanOrEqual(1);
      } else {
        expect(Math.abs(after!.x - box!.x)).toBeLessThanOrEqual(1);
      }
      assertClean();
    });
  }
});
