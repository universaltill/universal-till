import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2918: a phone held sideways (844x390, 896x414, 800x360) fell into
// the stacked `max-width: 900px` tier, where basket (capped at 45dvh),
// tender and products stacked in ~360px: the basket showed only its header
// and the Dine in / Takeaway toggle (no line, no totals) and Pay was cut in
// half. app.css now gives a short (max-height: 480px; 700px since
// ut-docs#5212, covered in tablet-tier-totals-416.spec.ts) window of 768-900px
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

  // ut-docs#5221: at 768-900px wide the tender column is ~251px of row, too
  // narrow for Pay (7rem) + three 2.75rem icons, so Open orders wrapped alone
  // onto a second line; and at <=540px tall the barcode field was squeezed to
  // a sliver beside qty, + and the camera/OSK/identify icons.
  const ACTION_ROW = [
    { width: 768, height: 400 },
    { width: 768, height: 480 },
    { width: 768, height: 600 },
    { width: 900, height: 414 }, // the column is wider here: one row stays one row
  ];
  const iconBoxes = (page: import('@playwright/test').Page) =>
    page.evaluate(() => {
      const box = (el: Element) => {
        const r = el.getBoundingClientRect();
        return { top: Math.round(r.top * 10) / 10, w: r.width, h: r.height };
      };
      const q = (s: string) => document.querySelector(s) as HTMLElement;
      const pay = q('.pos-container > .tender .payment-trigger');
      return {
        pay: { ...box(pay), clipped: pay.scrollWidth > pay.clientWidth },
        hold: box(q('[data-testid="tender-footer-hold"]')),
        newSale: box(q('[data-testid="kiosk-checkout-start"]')),
        open: box(q('[data-testid="parked-orders-open"]')),
        icons: Array.from(document.querySelectorAll('.pos-container > .tender .tender-icon-btn'))
          .filter((el) => (el as HTMLElement).offsetParent !== null)
          .map(box),
      };
    });

  for (const vp of ACTION_ROW) {
    test(`${vp.width}x${vp.height}: action row has Pay whole, every icon >= 44px, no lone icon (ut-docs#5221)`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);
      const pay = await wholeAndHit(payButton(page));
      expect(pay.inView && pay.hit, 'Pay whole and hit-testable').toBe(true);
      const g = await iconBoxes(page);
      expect(g.pay.clipped, 'Pay label is not ellipsised').toBe(false);
      for (const b of g.icons) {
        expect(b.w, 'icon width >= 44px').toBeGreaterThanOrEqual(43.9);
        expect(b.h, 'icon height >= 44px').toBeGreaterThanOrEqual(43.9);
      }
      expect(g.newSale.top, 'New sale on the same line as Hold').toBe(g.hold.top);
      expect(g.open.top, 'Open orders not alone on its own line').toBe(g.hold.top);
      if (vp.width === 900) expect(g.pay.top, 'one row stays one row').toBe(g.hold.top);
      else expect(g.pay.top, 'Pay has its own line (deliberate two-row layout)').not.toBe(g.hold.top);
      assertClean();
    });
  }

  for (const vp of [{ width: 768, height: 400 }, { width: 768, height: 480 }]) {
    test(`${vp.width}x${vp.height}: barcode field shows its placeholder, even with the OSK toggle and camera shown (ut-docs#5221)`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await addItems(page);
      for (const unhide of [false, true]) {
        if (unhide) {
          await page.evaluate(() => {
            document.querySelectorAll<HTMLElement>('[data-osk-toggle], #barcode-scan-open, #plugin-identify-open')
              .forEach((el) => { el.hidden = false; });
          });
        }
        const m = await page.locator('.scan-row input[name="code"]').evaluate((el: HTMLInputElement) => {
          const cs = getComputedStyle(el);
          const ctx = document.createElement('canvas').getContext('2d')!;
          ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
          const need = ctx.measureText(el.placeholder).width
            + parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)
            + parseFloat(cs.borderLeftWidth) + parseFloat(cs.borderRightWidth);
          return { width: el.getBoundingClientRect().width, need };
        });
        expect(m.width, `barcode field (unhidden=${unhide}) fits its placeholder`).toBeGreaterThanOrEqual(m.need);
        const g = await iconBoxes(page);
        for (const b of g.icons) {
          expect(b.w).toBeGreaterThanOrEqual(43.9);
          expect(b.h).toBeGreaterThanOrEqual(43.9);
        }
        // Everything but the + (qty's neighbour) shares one line: the icons.
        const secondary = g.icons.filter((b) => b.top === g.hold.top);
        expect(secondary.length, 'camera/OSK/identify/Hold/New/Open all on one line').toBe(g.icons.length - 1);
        expect(g.pay.top, 'Pay below the icons').toBeGreaterThan(g.hold.top);
        // No taller than the old scan row + two-line action row (180.47px at scale 1).
        const tenderH = await page.locator('.pos-container > .tender').evaluate((el) => el.getBoundingClientRect().height);
        expect(tenderH, 'tender must not grow').toBeLessThanOrEqual(180.5);
        const pay = await wholeAndHit(payButton(page));
        expect(pay.inView && pay.hit, 'Pay whole').toBe(true);
        const tile = await wholeAndHit(page.locator('.pos-container > .products :is(.category-tile, .tile, .btn-tile)').first());
        expect(tile.inView, 'a tile row still shows unscrolled').toBe(true);
      }
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
