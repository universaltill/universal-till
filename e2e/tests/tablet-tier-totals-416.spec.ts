import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#416: the @media (max-width: 900px) tier used to set
// `.pos-container { grid-template-rows: auto }` -- no flexible track, so
// align-content stretched basket/tender/products into equal rows and the
// basket's totals row rendered under the tender panel (the ut-docs#413
// mechanism, which #413 fixed only for the <=480px tier). ut-docs#2702 made
// that tier `auto auto minmax(8rem, 1fr)`, fixing it as a side effect; this
// spec was the regression test: reverting the rows to `auto` made the
// `short` viewports below fail (the probe landed on `.tender` or
// `.pos-container` instead of the totals row).
//
// Since ut-docs#5212 those short windows (<= 700px tall) no longer use the
// stacked tier at all: from 768px wide they get the side-by-side layout,
// narrower the pinned-tender layout (both from ut-docs#2918). The `auto`
// revert no longer fails them -- and no taller stacked window shows the
// #416 mechanism (re-measured with `auto` rows at 600x720, 768x720,
// 900x720, 880x760 and 768x800: all pass). The checks below still hold for
// the layouts these windows get now. The second describe block covers the
// grand-Total line (#5212).
//
// Scope, honestly: the probe checks the totals row's TOP edge (Subtotal).
// Scrolling a panel's own pane always resolves to totals whatever the row
// tracks are, so the `reach` viewports (too short to show totals
// unscrolled) only prove reachability.
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

// `short`: failed unscrolled with the pre-#2702 `auto` rows (verified); now
// side-by-side (ut-docs#5212). `pinned`: the same, now the pinned-tender
// layout, where the sticky tender may sit over the basket's bottom padding
// (it is pinned over the scroll by design), so basket/tender overlap is
// not asserted -- the totals probe still is.
// `smoke`: passes either way; covers the rest of the 500-900px range.
// `reach`: too short for totals unscrolled; scrolls `.basket` first.
const VIEWPORTS = [
  { width: 600, height: 600, label: 'small tablet, short', kind: 'pinned' },
  { width: 768, height: 600, label: 'tablet landscape-ish, short', kind: 'short' },
  { width: 880, height: 640, label: 'near the tier edge', kind: 'short' },
  { width: 900, height: 600, label: 'inclusive top edge of the max-width: 900px tier', kind: 'short' },
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

      if (vp.kind !== 'pinned') {
        expect(overlaps(geometry.basket!, geometry.tender!), 'basket must not overlap tender').toBe(false);
      }
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

// ut-docs#5212: below ~700px tall the stacked tier's 45dvh basket cap is
// smaller than the basket's min-content, so the grand Total line sat 15-25px
// below the basket's bottom edge, clipped inside its own scroll (900x600:
// basket bottom 276px, totals bottom 300px). These windows now use the
// short-window layouts of ut-docs#2918: from 768px wide the basket is a
// full-height column beside products and tender; narrower, the basket shows
// in full and the tender is pinned to the bottom of the sale screen's
// scroll. With the original CSS the 600x600, 768x540/541/600, 880x640 and
// 900x600 cases fail on the totals probe (verified); 900x700 and 600x690,
// where the old cap just fits, check the new layouts at their height edge.
// `side-by-side`: the Total line, the whole Pay button and a whole tile row
// show unscrolled. `pinned`: the Total line and Pay show unscrolled; at this
// width the tiles are one scroll away, and Pay stays whole after it. With
// 8 lines the side-by-side basket scrolls its lines inside .basket-scroll
// (Total still unscrolled); the pinned basket grows, so the Total line is
// one scroll away and must not end up under the pinned tender.
// Every case: the basket never scrolls sideways (the #3050 line shapes now
// meet the ~26rem basket column).
const SHORT_STACKED = [
  { width: 900, height: 600, kind: 'side-by-side' },
  { width: 768, height: 600, kind: 'side-by-side' },
  { width: 880, height: 640, kind: 'side-by-side' },
  { width: 768, height: 541, kind: 'side-by-side' }, // first height with the wrapped scan row
  { width: 768, height: 540, kind: 'side-by-side' },
  { width: 900, height: 700, kind: 'side-by-side' }, // inclusive height edge
  { width: 600, height: 600, kind: 'pinned' },
  { width: 600, height: 690, kind: 'pinned' },
  { width: 768, height: 600, kind: 'side-by-side', lines: 8 },
  { width: 600, height: 600, kind: 'pinned', lines: 8 },
] as const;

const MORE_CODES = ['5000000000036', '5000000000043', '5000000000050', '5000000000098', '5000000000104', '5000000000111'];

// Whole box inside the window and the element itself answers a probe at its
// centre (nothing painted over it).
async function wholeAndHit(locator: import('@playwright/test').Locator) {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const inView = r.top >= -1 && r.left >= -1 && r.bottom <= innerHeight + 1 && r.right <= innerWidth + 1 && r.height > 0;
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return { inView, hit: !!at && (at === el || el.contains(at)) };
  });
}

test.describe('short stacked tablet: the grand Total line is inside the basket (ut-docs#5212)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of SHORT_STACKED) {
    const lines = 'lines' in vp ? vp.lines : 2;
    test(`${vp.width}x${vp.height} (${vp.kind}, ${lines} lines): whole Total line, whole Pay, a tile row`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);
      if (lines > 2) {
        for (const code of MORE_CODES.slice(0, lines - 2)) {
          await page.locator('.scan-row input[name="code"]').fill(code);
          await Promise.all([
            page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
            page.locator('.scan-row button[type=submit]').click(),
          ]);
        }
        await expect(page.locator('#basket tbody tr')).toHaveCount(lines);
      }
      const sideways = await page.locator('.pos-container > .basket').evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(sideways, 'the basket must not scroll sideways').toBeLessThanOrEqual(1);
      if (vp.kind === 'pinned' && lines > 2) await page.locator('.basket .totals .total').scrollIntoViewIfNeeded();

      const geom = await page.evaluate(() => {
        const r = (sel: string) => document.querySelector(sel)!.getBoundingClientRect();
        return { basket: r('.pos-container > .basket').bottom, totals: r('.basket .totals').bottom, vh: innerHeight };
      });
      expect(geom.totals, 'the totals row must end inside the basket').toBeLessThanOrEqual(geom.basket + 0.5);
      expect(geom.totals, 'the totals row must end inside the window').toBeLessThanOrEqual(geom.vh);
      // The hit-test below is the discriminating check: in both layouts the
      // basket box itself is no longer capped below its content.
      const total = await wholeAndHit(page.locator('.basket .totals .total'));
      expect(total.inView && total.hit, 'the grand Total line must be whole and uncovered').toBe(true);

      const pay = page.locator('.pos-container > .tender button', { hasText: /Pay/ }).first();
      const p = await wholeAndHit(pay);
      expect(p.inView && p.hit, 'Pay must be whole and hit-testable').toBe(true);

      const tile = page.locator('.pos-container > .products :is(.category-tile, .tile, .btn-tile)').first();
      if (vp.kind === 'pinned') await tile.scrollIntoViewIfNeeded();
      const t = await wholeAndHit(tile);
      expect(t.inView && t.hit, 'a tile row must be whole and hit-testable').toBe(true);
      if (vp.kind === 'pinned') {
        const p2 = await wholeAndHit(pay);
        expect(p2.inView && p2.hit, 'Pay stays whole after scrolling to the tiles').toBe(true);
      }
      assertClean();
    });
  }
});

// ut-docs#5220: the stacked tier taller than 700px at UI scale > 1. The
// basket's min-content is in rem and grows with --ui-scale; its cap was in
// dvh and its grid row could squeeze it further, so from scale 1.25 the grand
// Total line sat below the basket's bottom edge (600x760, 900x720: basket
// bottom 330/317px, totals bottom 373/374px), and at 768x760 scale 1.5 the
// tender row shrank to 38px with Pay out of sight. With the original CSS
// every case below except 800x1280 fails (verified); 800x1280 guards the
// upright tablet's 38dvh cap, which already fit. `whole`: the Total line and
// Pay show unscrolled. `scroll`: ~22rem of height cannot hold the basket and
// the tender at once; the Total line stays inside the basket and is one
// scroll away and uncovered once scrolled to (at rest the pinned tender may
// cover it, by design), with Pay whole throughout. Every case: a tile is
// reachable by scrolling and Pay stays whole after that.
const SCALED = [
  ...[1.25, 1.5, 2].flatMap((scale) => [
    { width: 600, height: 760, scale, kind: 'whole' },
    { width: 768, height: 800, scale, kind: 'whole' },
    { width: 800, height: 1280, scale, kind: 'whole' },
  ]),
  { width: 900, height: 720, scale: 1.25, kind: 'whole' },
  { width: 900, height: 720, scale: 1.5, kind: 'whole' },
  { width: 900, height: 720, scale: 2, kind: 'scroll' },
  { width: 880, height: 960, scale: 2, kind: 'whole' },
  { width: 768, height: 760, scale: 1.5, kind: 'whole', lines: 8 },
  { width: 900, height: 701, scale: 2, kind: 'scroll', lines: 8 },
] as const;

test.describe('stacked tablet at UI scale > 1: the grand Total line is inside the basket (ut-docs#5220)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of SCALED) {
    const lines = 'lines' in vp ? vp.lines : 2;
    test(`${vp.width}x${vp.height} ui_scale ${vp.scale} (${vp.kind}, ${lines} lines): Total inside the basket, whole Pay`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      // --ui-scale grows rem, not the viewport (as portrait-tablet-sale-3050).
      await page.evaluate((s) => document.documentElement.style.setProperty('--ui-scale', String(s)), vp.scale);
      await addItems(page);
      for (const code of MORE_CODES.slice(0, lines - 2)) {
        await page.locator('.scan-row input[name="code"]').fill(code);
        await Promise.all([
          page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
          page.locator('.scan-row button[type=submit]').click(),
        ]);
      }
      await expect(page.locator('#basket tbody tr')).toHaveCount(lines);

      const sideways = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      expect(sideways, 'the sale screen must not scroll sideways').toBeLessThanOrEqual(1);
      const geom = await page.evaluate(() => {
        const r = (sel: string) => document.querySelector(sel)!.getBoundingClientRect();
        return { basket: r('.pos-container > .basket').bottom, totals: r('.basket .totals').bottom };
      });
      expect(geom.totals, 'the totals row must end inside the basket').toBeLessThanOrEqual(geom.basket + 0.5);

      const pay = page.locator('.pos-container > .tender button', { hasText: /Pay/ }).first();
      const p = await wholeAndHit(pay);
      expect(p.inView && p.hit, 'Pay must be whole and hit-testable').toBe(true);

      const totalLine = page.locator('.basket .totals .total');
      if (vp.kind === 'scroll') {
        await totalLine.evaluate((el) => el.scrollIntoView({ block: 'center' }));
      }
      const total = await wholeAndHit(totalLine);
      expect(total.inView && total.hit, 'the grand Total line must be whole and uncovered').toBe(true);
      if (vp.kind === 'scroll') {
        const p2 = await wholeAndHit(pay);
        expect(p2.inView && p2.hit, 'Pay stays whole after scrolling to the Total line').toBe(true);
      }
      // The products stay reachable under the pinned tender, and Pay stays
      // whole once they are in view.
      const tile = page.locator('.pos-container > .products :is(.category-tile, .tile, .btn-tile)').first();
      await tile.scrollIntoViewIfNeeded();
      const t = await wholeAndHit(tile);
      expect(t.inView && t.hit, 'a tile must be whole and hit-testable').toBe(true);
      const p3 = await wholeAndHit(pay);
      expect(p3.inView && p3.hit, 'Pay stays whole after scrolling to the tiles').toBe(true);
      assertClean();
    });
  }
});
