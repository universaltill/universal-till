import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3050: the sale screen on an upright tablet (481-900px wide). The
// stacked `max-width: 900px` tier used to spend ~119px per basket line (qty
// input, -/+ row and discount stacked in three rows) and cap the basket at
// 45dvh, so an 800x1280 tablet showed two basket lines and ~2.5 rows of
// product tiles. app.css now lays a line out by the basket's own width in
// rem (a container query): one row at 800px, name above the controls at
// 600px. Upright tablets also get a lower basket cap. Landscape (>= 901px),
// landscape phones and the <=480px phone tier (ut-docs#3059) are untouched.
// Demo-seed barcodes. Some are variant items: their scan opens the
// variant picker, and scan() takes the first variant.
const CODES = [
  '5000000000012', '5000000000029', '5000000000036', '5000000000043', '5000000000050',
  '5000000000067', '5000000000074', '5000000000081', '5000000000098',
];

async function scan(page: Page, codes: string[], expectLines: number) {
  for (const code of codes) {
    await page.locator('.scan-row input[name="code"]').fill(code);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    const variant = page.locator('#modifier-modal input[name="variantId"]').first();
    if (await variant.isVisible()) {
      await variant.check();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers')),
        page.locator('#modifier-modal button[type=submit]').click(),
      ]);
    }
  }
  await expect(page.locator('#basket tbody tr')).toHaveCount(expectLines);
}

async function useDarkTheme(page: Page) {
  await page.evaluate(async () => {
    const link = document.getElementById('theme-css') as HTMLLinkElement;
    await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
  });
}

// Every measurement the AC names, in one round trip.
async function measure(page: Page) {
  return page.evaluate(() => {
    const r = (el: Element | null) => el ? el.getBoundingClientRect() : null;
    const lines = [...document.querySelectorAll('#basket tbody tr')].map((tr) => r(tr)!.height);
    const steps = [...document.querySelectorAll('#basket .qty-step-btn')].map((b) => {
      const x = r(b)!; return { w: x.width, h: x.height };
    });
    const products = r(document.querySelector('.pos-container > .products'))!;
    const pay = document.querySelector('[data-testid="payment-open"]') as HTMLElement | null;
    const payRect = r(pay);
    const hit = payRect
      ? document.elementFromPoint(payRect.left + payRect.width / 2, payRect.top + payRect.height / 2)
      : null;
    return {
      scrollH: document.documentElement.scrollHeight,
      clientH: document.documentElement.clientHeight,
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
      lines,
      steps,
      productsH: products.height,
      payBottom: payRect ? payRect.bottom : -1,
      payHit: !!(pay && hit && (hit === pay || pay.contains(hit))),
      nameW: Math.min(...[...document.querySelectorAll('#basket .line-name')].map((n) => r(n)!.width)),
    };
  });
}

// maxLine: a 10in tablet fits a line on one row (was ~119px); a 7-8in
// tablet puts the full-width name on its own row above the controls.
// minProducts: products' share of the viewport height with 2 lines. At
// 600x960 the basket's fixed chrome (header, order type, totals ~180px)
// plus the tender panel (~127px) leave ~40% at best, ~38% once the status
// bar wraps to two rows (Update chip showing). minProductsLong: the same
// with a full basket, once the basket reaches its 38dvh cap.
const VIEWPORTS = [
  { width: 800, height: 1280, label: '10in tablet portrait', maxLine: 64, minProducts: 0.55, minProductsLong: 0.45 },
  { width: 600, height: 960, label: '7-8in tablet portrait', maxLine: 110, minProducts: 0.37, minProductsLong: 0.37 },
];

test.describe('portrait tablet sale screen (ut-docs#3050)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of VIEWPORTS) {
    for (const lang of ['en', 'fa']) {
      for (const theme of ['light', 'dark']) {
        test(`${vp.width}x${vp.height} ${lang} ${theme}: one-row lines, products get the height, Pay reachable`, async ({ page }) => {
          const assertClean = watchConsole(page);
          await page.setViewportSize({ width: vp.width, height: vp.height });
          await page.goto(`/?lang=${lang}`);
          await page.waitForSelector('.pos-container');
          if (theme === 'dark') await useDarkTheme(page);

          // Two lines: the everyday case.
          await scan(page, CODES.slice(0, 2), 2);
          let m = await measure(page);
          expect(m.scrollH, 'page must not scroll').toBeLessThanOrEqual(m.clientH);
          expect(m.scrollW, 'no horizontal page scroll').toBeLessThanOrEqual(m.clientW);
          for (const h of m.lines) expect(h, 'basket line height').toBeLessThanOrEqual(vp.maxLine);
          for (const s of m.steps) {
            expect(s.h, 'stepper button height').toBeGreaterThanOrEqual(46);
            expect(s.w, 'stepper button width').toBeGreaterThanOrEqual(46);
          }
          expect(m.nameW, 'item name keeps a usable width').toBeGreaterThanOrEqual(120);
          expect(m.productsH / vp.height, 'products share of the screen').toBeGreaterThanOrEqual(vp.minProducts);

          // A long basket scrolls inside itself; Pay stays on screen.
          await scan(page, CODES.slice(2), CODES.length);
          m = await measure(page);
          expect(m.scrollH, 'page must not scroll with a long basket').toBeLessThanOrEqual(m.clientH);
          expect(m.payBottom).toBeGreaterThan(0);
          expect(m.payBottom, 'Pay inside the viewport').toBeLessThanOrEqual(vp.height);
          expect(m.payHit, 'Pay is hit-testable (not covered)').toBe(true);
          expect(m.productsH / vp.height, 'products keep their share with a long basket').toBeGreaterThanOrEqual(vp.minProductsLong);
          assertClean();
        });
      }
    }
  }

  // Review findings (ut-docs#3050): the independent review measured each of
  // these broken on the first (viewport-query) version of this CSS.
  test('800x1280 kiosk: the qty and discount inputs keep the 46px floor beside the steppers', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 1280 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CODES.slice(0, 2), 2);
    await page.evaluate(() => document.body.classList.add('kiosk'));
    const h = await page.evaluate(() =>
      [...document.querySelectorAll('#basket .qty-input, #basket .disc-input')].map((e) => e.getBoundingClientRect().height));
    for (const x of h) expect(x).toBeGreaterThanOrEqual(46);
  });

  // --ui-scale grows rem but not the viewport. The basket's line layout is a
  // container query in rem, so a scaled till drops to a roomier shape
  // instead of overflowing (measured on the viewport-query version: at 800px
  // scale 1.5 the name column was 0px wide; at scale 2 the remove button
  // sat at x=940 on an 800px screen).
  for (const scale of ['1.25', '1.5']) {
    test(`800x1280 ui_scale ${scale}: no basket overflow, the name stays readable`, async ({ page }) => {
      await page.setViewportSize({ width: 800, height: 1280 });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await scan(page, CODES.slice(0, 2), 2);
      await page.evaluate((s) => document.documentElement.style.setProperty('--ui-scale', s), scale);
      const m = await page.evaluate(() => {
        const sc = document.querySelector('.basket-scroll')!;
        return {
          over: sc.scrollWidth - sc.clientWidth,
          name: Math.min(...[...document.querySelectorAll('#basket .line-name')].map((n) => n.getBoundingClientRect().width)),
          removeRight: Math.max(...[...document.querySelectorAll('#basket tbody .btn-x')].map((n) => n.getBoundingClientRect().right)),
        };
      });
      expect(m.over, 'basket must not scroll sideways').toBeLessThanOrEqual(0);
      expect(m.name, 'item name width').toBeGreaterThanOrEqual(80);
      expect(m.removeRight, 'remove button on screen').toBeLessThanOrEqual(800);
    });
  }

  // The Android keyboard shrinks the viewport (~40% of the height). An
  // orientation query flipped to landscape mid-typing (lines 58 -> 119px,
  // steppers 46 -> 27px wide).
  test('800x1280 -> 800x760 (keyboard open): the line layout does not change', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 1280 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CODES.slice(0, 2), 2);
    const before = await page.locator('#basket tbody tr').first().evaluate((e) => e.getBoundingClientRect().height);
    await page.setViewportSize({ width: 800, height: 760 });
    const after = await page.evaluate(() => ({
      line: document.querySelector('#basket tbody tr')!.getBoundingClientRect().height,
      step: document.querySelector('#basket .qty-step-btn')!.getBoundingClientRect().width,
    }));
    expect(after.line).toBeCloseTo(before, 0);
    expect(after.step).toBeGreaterThanOrEqual(46);
  });

  // Found by the full suite: once the update check shows the "Update now"
  // chip, the status bar (Online, Marketplace, Update, version) ran to
  // 712px at 600px wide and scrolled the page sideways. The chip is
  // server-rendered only when a release is newer, so inject one shaped
  // like it (and a register chip, the other conditional item).
  test('600x960: a full status bar wraps instead of scrolling the page sideways', async ({ page }) => {
    await page.setViewportSize({ width: 600, height: 960 });
    await page.goto('/');
    await page.waitForSelector('.statusbar');
    await page.evaluate(() => {
      const bar = document.querySelector('.statusbar')!;
      const ver = bar.querySelector('.sb-ver');
      for (const [cls, text] of [['sb-item sb-update', 'Update now v99.99.99'], ['sb-item sb-enrol', 'Register this till']]) {
        const a = document.createElement('a');
        a.className = cls; a.textContent = text; a.href = '#';
        bar.insertBefore(a, ver);
      }
    });
    const m = await page.evaluate(() => ({
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
      barRight: Math.max(...[...document.querySelectorAll('.statusbar > *')].map((e) => e.getBoundingClientRect().right)),
    }));
    expect(m.scrollW, 'no horizontal page scroll').toBeLessThanOrEqual(m.clientW);
    expect(m.barRight, 'every status item inside the viewport').toBeLessThanOrEqual(600);
  });

  test('landscape 1280x800 keeps its one-row-per-column layout (no portrait rules leak)', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CODES.slice(0, 2), 2);
    // The landscape line keeps its stacked qty column (qty / -+ / discount).
    const dir = await page.locator('#basket .line-inputs').first().evaluate((e) => getComputedStyle(e).flexDirection);
    expect(dir).toBe('column');
  });
});
