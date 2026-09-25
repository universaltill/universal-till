import { test, expect } from './fixtures';
import { watchConsole, clearAllHeldSales } from './helpers';

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

    // ut-docs#1252: scan the item FIRST, matching the real operator flow
    // (build the basket, then open Payment). ut-docs#1385: the payment
    // overlay used to be a MODAL <dialog> that blocked pointer events on
    // the rest of the page (including the scan-row, which stays outside
    // it, in .tender's default view) while open -- opening the overlay
    // before scanning would have hung waiting for a click the modal itself
    // was blocking. It's non-modal now (.show(), not .showModal() -- the
    // on-screen keyboard needed to stay tappable while it's open), so that
    // particular hang can't happen any more either way, but scan-then-open
    // still matches the real flow and is kept unchanged.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // The Pay/Split tabs now live inside the #payment-overlay dialog,
    // opened by the .payment-trigger button, instead of always being on
    // screen -- open it before probing .tab-panel's geometry.
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

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
    await page.goto('/settings#settings-display'); // ut-docs#1960: Settings is two-pane now — deep-link to the section this drives
    const scaleSelect = page.locator('form[hx-post="/api/settings/ui-scale"] select');
    await scaleSelect.selectOption('2');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
      scaleSelect.locator('..').locator('button[type=submit]').click(),
    ]);
    await page.waitForEvent('load');

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // ut-docs#1252: same overlay-open precondition as the test above.
    // ut-docs#1984: the Payment button is now genuinely disabled on an
    // empty basket, so it must be scanned into non-empty first — same
    // scan-then-open sequence as the test above.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

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
    await page.goto('/settings#settings-display');
    const restore = page.locator('form[hx-post="/api/settings/ui-scale"] select');
    await restore.selectOption('1');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale')),
      restore.locator('..').locator('button[type=submit]').click(),
    ]);
    await page.waitForEvent('load');
    // e2e/README: a spec that adds basket items must complete its sale or
    // explicitly clear it — this test only hit-tests, never taps Cash, so
    // ut-docs#1984's scan-first item above must be cleared explicitly.
    await page.request.post('/api/pos/reset');
    assertClean();
  });

  // 2026-08-30 (independent review): the default view's action row has its
  // OWN clipping failure mode the two tests above can't see -- they open the
  // overlay first and only assert on elements INSIDE it. The row itself
  // lives in the always-visible default view, exposed to `.tender`'s own
  // height budget. Real regressions caught live before this test existed:
  // the Payment button clipped at 1024x600 with an empty till, and a
  // growing held-sales strip pushing it off-screen.
  // ut-docs#2702: the strip and the quick-pay row are gone and the whole
  // action row is ONE line (Pay + Hold / New sale / Open orders icons), so
  // every control in it is hit-tested at its bottom edge (the most clip-
  // exposed point), with 0 and 3 held sales -- held sales now only move
  // the Open orders badge, and must never move the row.
  const ACTION_ROW = ['payment-open', 'tender-footer-hold', 'kiosk-checkout-start', 'parked-orders-open'];
  const bottomEdgeHit = (page: import('@playwright/test').Page, testid: string) =>
    page.getByTestId(testid).evaluate((el) => {
      const r = el.getBoundingClientRect();
      const x = r.left + r.width / 2;
      const y = r.bottom - 2;
      if (y > window.innerHeight || y < 0 || x < 0 || x > window.innerWidth) return false;
      const at = document.elementFromPoint(x, y);
      return !!at && (at === el || el.contains(at));
    });
  const holdOneSale = async (page: import('@playwright/test').Page, code: string) => {
    await page.locator('input[name="code"]').first().fill(code);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.getByTestId('tender-footer-hold').click();
    const modal = page.locator('#hold-modal');
    await expect(modal).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      modal.locator('button[type=submit]').click(),
    ]);
    await expect(modal).toBeHidden();
  };

  for (const vp of [
    { width: 1024, height: 600, label: '1024x600 (kiosk floor)' },
    { width: 1280, height: 800, label: '1280x800 (pilot tablet)' },
    // ut-docs#1327: the 900px-width stacked tablet tier (basket/tender/
    // products in one column) clipped the Payment button independently of
    // the height-driven kiosk-floor bug -- width-driven, not height-driven.
    { width: 850, height: 700, label: '850x700 (stacked tablet tier)' },
  ]) {
    test(`the action row is never clipped at ${vp.label}, with or without held sales`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await clearAllHeldSales(page);
      try {
        for (const id of ACTION_ROW) {
          expect(await bottomEdgeHit(page, id), `${id} must be unclipped with no held sales, no scroll`).toBe(true);
        }
        const codes = ['5000000000012', '5000000000029', '5000000000012'];
        for (let i = 0; i < codes.length; i++) {
          await holdOneSale(page, codes[i]);
          for (const id of ACTION_ROW) {
            expect(await bottomEdgeHit(page, id), `${id} must stay unclipped with ${i + 1} held sale(s)`).toBe(true);
          }
        }
        // >= not ==: other specs on this shared server may park orders too.
        await expect.poll(async () => Number(await page.getByTestId('open-orders-badge').getAttribute('data-count'))).toBeGreaterThanOrEqual(3);
      } finally {
        // Held sales are persistent DB rows shared by every spec on this
        // server -- leave none behind, pass or fail.
        await clearAllHeldSales(page);
      }
      assertClean();
    });
  }

  // 360px phone tier (ut-docs#413's breakpoint): .pos-container is
  // deliberately scrollable at this width ("never invisible, always
  // reachable via scroll"), so the contract here is reachability after a
  // scroll plus no horizontal overflow -- not the no-scroll guarantee the
  // tests above hold.
  test('the action row is reachable at 360px phone width with no horizontal overflow', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 640 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(overflow.scrollWidth, 'the action row must not widen the page past 360px').toBeLessThanOrEqual(overflow.clientWidth);

    for (const id of ACTION_ROW) {
      const el = page.getByTestId(id);
      await el.scrollIntoViewIfNeeded();
      const hit = await el.evaluate((node) => {
        const r = node.getBoundingClientRect();
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!at && (at === node || node.contains(at));
      });
      expect(hit, `${id} must be a real hit-test target at 360px width`).toBe(true);
    }
    assertClean();
  });

  // RTL (fa): the row is DOM-ordered Pay, Hold, New sale, Open orders with
  // no left/right literals, so under dir="rtl" Pay must take the inline
  // START (the right edge) and the icons must run leftwards after it. A
  // stray left/right in the row's CSS would leave it unmirrored and fail.
  test('the action row mirrors under RTL (fa) and every control stays a real target', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await page.waitForSelector('.pos-container');

    const geom = await page.evaluate((ids) => {
      const row = document.querySelector('.tender-default-footer')!.getBoundingClientRect();
      return {
        rowLeft: row.left,
        rowRight: row.right,
        boxes: ids.map((id) => {
          const r = document.querySelector(`[data-testid="${id}"]`)!.getBoundingClientRect();
          return { left: r.left, right: r.right };
        }),
      };
    }, ACTION_ROW);
    expect(Math.abs(geom.boxes[0].right - geom.rowRight), 'Pay must lead at the row start (RTL = right edge)').toBeLessThan(2);
    expect(Math.abs(geom.boxes[3].left - geom.rowLeft), 'Open orders must end the row (RTL = left edge)').toBeLessThan(2);
    for (let i = 1; i < geom.boxes.length; i++) {
      expect(geom.boxes[i].right, `${ACTION_ROW[i]} must sit inline-after ${ACTION_ROW[i - 1]} under RTL`).toBeLessThanOrEqual(geom.boxes[i - 1].left + 1);
    }
    for (const id of ACTION_ROW) {
      expect(await bottomEdgeHit(page, id), `${id} must be a real hit-test target under RTL`).toBe(true);
    }
    assertClean();
  });
});
