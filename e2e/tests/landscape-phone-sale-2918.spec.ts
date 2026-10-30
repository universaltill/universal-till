import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2918: a phone held sideways (844x390, 896x414, 800x360) fell into
// the stacked `max-width: 900px` tier, where basket (capped at 45dvh),
// tender and products stacked in ~360px: the basket showed only its header
// and the Dine in / Takeaway toggle (no line, no totals) and Pay was cut in
// half. app.css now gives a short (max-height: 480px) window of 768-900px
// the desktop arrangement (basket column | products over tender), and a
// narrower short one (667x375, 740x360) a stacked, scrolling screen with an
// uncapped basket and the tender pinned to the bottom. Without the fix the
// `side-by-side` cases fail on the line/totals/Pay probes and the `pinned`
// cases on the Pay probe.
const CODES = ['5000000000012', '5000000000029'];

type Box = { top: number; bottom: number; left: number; right: number };

async function addItems(page: import('@playwright/test').Page) {
  for (const code of CODES) {
    await page.locator('.scan-row input[name="code"]').fill(code);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
  }
  await expect(page.locator('#basket')).toContainText('Pepsi');
}

// Whole box inside the viewport and the element itself answers a probe at
// its centre (nothing painted over it).
async function wholeAndHit(locator: import('@playwright/test').Locator) {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    // 1px slack: scrollIntoView can leave a sub-pixel negative top.
    const inView = r.top >= -1 && r.left >= -1 && r.bottom <= innerHeight + 1 && r.right <= innerWidth + 1 && r.height > 0;
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    const hit = !!at && (at === el || el.contains(at));
    return { inView, hit, box: { top: r.top, bottom: r.bottom, left: r.left, right: r.right } as Box };
  });
}

async function setUiScale(page: import('@playwright/test').Page, value: string) {
  await page.goto('/settings#settings-display');
  const select = page.locator('form[hx-post="/api/settings/ui-scale"] select');
  await select.selectOption(value);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
    select.locator('..').locator('button[type=submit]').click(),
  ]);
  await page.waitForEvent('load');
}

const payButton = (page: import('@playwright/test').Page) =>
  page.locator('.pos-container > .tender button', { hasText: /Pay/ }).first();

const SIDE_BY_SIDE = [
  { width: 844, height: 390 },
  { width: 896, height: 414 },
  { width: 800, height: 360 },
  { width: 768, height: 400 },
  { width: 900, height: 414 }, // inclusive top edge of the 900px tier
  { width: 768, height: 480 }, // inclusive height edge (481+ is the tablet tier)
];
const PINNED = [
  { width: 667, height: 375 },
  { width: 740, height: 360 },
];

test.describe('landscape phone sale screen (ut-docs#2918)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of SIDE_BY_SIDE) {
    test(`${vp.width}x${vp.height}: a line, the totals, the whole Pay button and a tile row show unscrolled`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);

      const basket = await page.locator('.pos-container > .basket').evaluate((el) => {
        const r = el.getBoundingClientRect();
        return { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
      });

      // The first line's name is on screen and not under another panel.
      const line = await wholeAndHit(page.locator('.basket tbody tr .line-name').first());
      expect(line.hit, 'first basket line must be visible, not covered').toBe(true);
      expect(line.box.top).toBeGreaterThanOrEqual(basket.top);
      expect(line.box.bottom, 'first line must sit inside the basket').toBeLessThanOrEqual(basket.bottom);

      // The whole totals box, grand Total included, inside the basket.
      const totals = await wholeAndHit(page.locator('.basket .totals'));
      expect(totals.inView, 'totals must be wholly on screen').toBe(true);
      expect(totals.hit, 'totals must not be covered').toBe(true);
      expect(totals.box.bottom, 'grand Total must not be clipped by the basket').toBeLessThanOrEqual(basket.bottom + 0.5);

      const pay = await wholeAndHit(payButton(page));
      expect(pay.inView, 'Pay must be whole on screen').toBe(true);
      expect(pay.hit, 'Pay must be hit-testable').toBe(true);

      // At least one product/category tile row is visible without scrolling.
      const tile = await wholeAndHit(page.locator('.pos-container > .products :is(.category-tile, .tile, .btn-tile)').first());
      expect(tile.inView, 'a first row of tiles must show unscrolled').toBe(true);

      // Basket column beside products/tender, and no sideways page scroll.
      const tender = await page.locator('.pos-container > .tender').evaluate((el) => el.getBoundingClientRect().left);
      expect(tender, 'tender sits beside the basket').toBeGreaterThanOrEqual(basket.right);
      const overflowX = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      expect(overflowX, 'no horizontal page overflow').toBeLessThanOrEqual(0);
      assertClean();
    });
  }

  for (const vp of PINNED) {
    test(`${vp.width}x${vp.height}: Pay pinned whole; one scroll shows the totals`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);

      const pay = await wholeAndHit(payButton(page));
      expect(pay.inView, 'Pay must be whole on screen unscrolled').toBe(true);
      expect(pay.hit, 'Pay must be hit-testable unscrolled').toBe(true);

      // The first line's name shows above the pinned tender.
      const line = await wholeAndHit(page.locator('.basket tbody tr .line-name').first());
      expect(line.hit, 'first basket line must be visible above the tender').toBe(true);

      // One scroll of the sale screen brings the totals clear of the tender,
      // and Pay stays whole and in reach.
      await page.locator('.basket .totals').evaluate((el) => el.scrollIntoView({ block: 'start' }));
      const totals = await wholeAndHit(page.locator('.basket .totals'));
      expect(totals.inView, 'totals reachable by scrolling the sale screen').toBe(true);
      expect(totals.hit, 'totals not covered by the pinned tender once scrolled').toBe(true);
      const payAfter = await wholeAndHit(payButton(page));
      expect(payAfter.inView && payAfter.hit, 'Pay still whole after scrolling').toBe(true);

      const overflowX = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      expect(overflowX, 'no horizontal page overflow').toBeLessThanOrEqual(0);
      assertClean();
    });
  }

  // Review finding: the side-by-side grid needs ~46rem of width, so a manual
  // UI scale (Settings -> Display) must shrink to fit here as it does above
  // 900px (ut-docs#4625's --ui-scale-min-width). Without that, 115% already
  // scrolled the page sideways and 130%+ pushed the basket under the rail.
  for (const scale of ['1.3', '1.5']) {
    for (const vp of [{ width: 768, height: 480 }, { width: 844, height: 390 }, { width: 900, height: 414 }]) {
      test(`${vp.width}x${vp.height} at UI scale ${scale}: no sideways scroll, basket clear of the rail, Pay whole`, async ({ page }) => {
        await page.setViewportSize(vp);
        try {
          await setUiScale(page, scale);
          await page.goto('/');
          await page.waitForSelector('.pos-container');
          await addItems(page);
          const g = await page.evaluate(() => ({
            overflowX: document.documentElement.scrollWidth - innerWidth,
            navRight: (document.querySelector('.nav') as HTMLElement).getBoundingClientRect().right,
            basketLeft: (document.querySelector('.pos-container > .basket') as HTMLElement).getBoundingClientRect().left,
          }));
          expect(g.overflowX, 'no horizontal page overflow at a high UI scale').toBeLessThanOrEqual(0);
          expect(g.basketLeft, 'basket must not slide under the nav rail').toBeGreaterThanOrEqual(g.navRight);
          const pay = await wholeAndHit(payButton(page));
          expect(pay.inView && pay.hit, 'Pay whole and hit-testable at a high UI scale').toBe(true);
        } finally {
          await setUiScale(page, '1');
        }
      });
    }
  }
});
