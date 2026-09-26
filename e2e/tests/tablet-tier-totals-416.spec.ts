import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#416: the @media (max-width: 900px) tier used to set
// `.pos-container { grid-template-rows: auto }` -- no flexible track, so
// align-content stretched basket/tender/products into equal rows and the
// basket's totals row rendered under the tender panel (the ut-docs#413
// mechanism, which #413 fixed only for the <=480px tier). ut-docs#2702 made
// that tier `auto auto minmax(8rem, 1fr)`, fixing it as a side effect; this
// spec is the regression test. Reverting the rows to `auto` makes the
// `regression` viewports below fail: the probe lands on `.tender`
// (or `.pos-container`) instead of the totals row.
//
// Scope, honestly: the probe checks the totals row's TOP edge (Subtotal).
// At short stacked heights (<= ~640px) the grand-Total line is still
// partly clipped by `.basket`'s own `max-height: 45dvh` -- a different,
// pre-existing mechanism tracked on ut-docs#2918 together with the
// landscape-phone layout. Scrolling a panel's own pane always resolves to
// totals whatever the row tracks are, so the `reach` viewports (too short to
// show totals unscrolled) only prove reachability, not the #416 fix.
const CODES = ['5000000000012', '5000000000029'];

async function addItems(page: import('@playwright/test').Page) {
  for (const code of CODES) {
    await page.locator('.scan-row input[name="code"]').fill(code);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
  }
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
}

function overlaps(
  a: { top: number; bottom: number; left: number; right: number },
  b: { top: number; bottom: number; left: number; right: number },
) {
  return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
}

// `regression`: fails unscrolled with the pre-#2702 `auto` rows (verified).
// `smoke`: passes either way; covers the rest of the 500-900px range.
// `reach`: too short for totals unscrolled; scrolls `.basket` first.
const VIEWPORTS = [
  { width: 600, height: 600, label: 'small tablet, short', kind: 'regression' },
  { width: 768, height: 600, label: 'tablet landscape-ish, short', kind: 'regression' },
  { width: 880, height: 640, label: 'near the tier edge', kind: 'regression' },
  { width: 900, height: 600, label: 'inclusive top edge of the max-width: 900px tier', kind: 'regression' },
  { width: 540, height: 720, label: 'small tablet portrait', kind: 'smoke' },
  { width: 600, height: 960, label: 'tall narrow tablet portrait', kind: 'smoke' },
  { width: 768, height: 1024, label: 'iPad portrait', kind: 'smoke' },
  { width: 896, height: 414, label: 'landscape phone', kind: 'reach' },
  { width: 844, height: 390, label: 'landscape phone, shorter', kind: 'reach' },
];

test.describe('tablet-tier totals row never obscured (ut-docs#416)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of VIEWPORTS) {
    test(`${vp.width}x${vp.height} (${vp.label}): totals row visible, hit-testable, panels don't overlap`, async ({
      page,
    }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);

      const totals = page.locator('.basket .totals');
      await expect(totals).toBeVisible();

      if (vp.kind === 'reach') {
        // Scrolls `.basket`'s own pane only (see the file header).
        await totals.scrollIntoViewIfNeeded();
      }

      const geometry = await page.evaluate(() => {
        const rect = (sel: string) => {
          const el = document.querySelector(sel);
          if (!el) return null;
          const r = el.getBoundingClientRect();
          return { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
        };
        return {
          basket: rect('.pos-container > .basket'),
          tender: rect('.pos-container > .tender'),
          products: rect('.pos-container > .products'),
        };
      });
      expect(geometry.basket, 'basket panel must render').not.toBeNull();
      expect(geometry.tender, 'tender panel must render').not.toBeNull();

      expect(overlaps(geometry.basket!, geometry.tender!), 'basket must not overlap tender').toBe(false);
      if (geometry.products) {
        expect(overlaps(geometry.basket!, geometry.products), 'basket must not overlap products').toBe(false);
        expect(overlaps(geometry.tender!, geometry.products), 'tender must not overlap products').toBe(false);
      }

      // Same class of assertion as phone-width-layout-413.spec.ts's own
      // "the Subtotal/Total row is visible and not obscured" test: probe
      // near the totals row's own top edge and require the hit to resolve
      // to totals itself, not another panel rendered on top of it.
      // Name what the probe hit, so a failure says which panel covers totals.
      const hitOwner = await totals.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const cx = r.left + r.width / 2;
        const cy = r.top + Math.min(10, r.height / 2);
        const at = document.elementFromPoint(cx, cy);
        if (!at) return 'nothing';
        if (at === el || el.contains(at)) return 'totals';
        return at.closest('.pos-container > *')?.className || at.tagName;
      });
      expect(hitOwner, 'totals row must not be covered by another panel').toBe('totals');
      assertClean();
    });
  }
});
