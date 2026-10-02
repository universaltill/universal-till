import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3132: /users/permissions is a wide action × role grid. Above the
// phone tier, when it doesn't fit it scrolls sideways inside its own card
// (never the page), with the action column pinned at the card's
// inline-start edge so a checkbox is never read against the wrong action —
// in LTR and RTL. Measured, not eyeballed: page scrollWidth vs clientWidth,
// and the pinned column's box before/after scrolling the grid to its far
// end. At the phone tier (≤480px) ut-docs#3359 replaced the sideways
// scroll with one card per action — see the second describe below.

const CASES = [
  { width: 1024, height: 600, lang: 'en', mustScroll: false },
  { width: 520, height: 740, lang: 'en', mustScroll: true },
  { width: 520, height: 740, lang: 'fa', mustScroll: true },
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

// ut-docs#3359: on a phone nothing scrolls sideways — each action is a
// card whose every role checkbox sits on its own "ROLE ☐" line (none waits
// behind More: the table is data-cards-collapse="off"), in LTR and RTL.
test.describe('permissions matrix as phone cards (ut-docs#3359)', () => {
  for (const lang of ['en', 'fa']) {
    test(`/users/permissions at 360x740 (${lang}): one card per action, every role labelled`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 740 });
      const assertClean = watchConsole(page);
      await page.goto(`/users/permissions?lang=${lang}`);
      await expect(page.locator('table.perm-table.t-cards')).toBeVisible();
      const r = await page.evaluate(() => {
        const roles = Array.from(document.querySelectorAll('.perm-table thead th.perm-cell')).map((th) => (th.textContent || '').trim());
        const card = document.querySelector('.perm-table tbody tr:has(th.perm-action)')!;
        const box = card.getBoundingClientRect();
        const cells = Array.from(card.querySelectorAll('td.perm-cell')).map((td) => {
          const b = td.getBoundingClientRect();
          return { label: getComputedStyle(td, '::before').content, visible: b.height > 0, inside: b.left >= box.left - 1 && b.right <= box.right + 1 };
        });
        return {
          roles, cells, more: card.querySelectorAll('.t-cards-toggle').length,
          sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth,
          scrolls: (() => { const s = document.querySelector('.perm-scroll')!; return s.scrollWidth > s.clientWidth + 1; })(),
        };
      });
      expect(r.sw, 'page must not scroll horizontally').toBeLessThanOrEqual(r.cw);
      expect(r.scrolls, 'the grid must not scroll sideways on a phone').toBe(false);
      expect(r.more, 'every role stays on the card').toBe(0);
      expect(r.cells.map((c) => c.label)).toEqual(r.roles.map((t) => JSON.stringify(t)));
      for (const c of r.cells) expect(c.visible && c.inside, JSON.stringify(c)).toBe(true);
      assertClean();
    });
  }
});
