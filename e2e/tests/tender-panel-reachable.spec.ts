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

    // ut-docs#1252: the payment overlay is a MODAL <dialog> -- it blocks
    // pointer events on the rest of the page (including the scan-row,
    // which stays outside it, in .tender's default view) while open, same
    // as this file's other dialogs. So scan the item FIRST, matching the
    // real operator flow (build the basket, then open Payment) -- opening
    // the overlay before scanning would just hang waiting for a click the
    // modal itself is blocking.
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

    // ut-docs#1252: same overlay-open precondition as the test above.
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

  // 2026-08-30 (independent review, product-owner UX comparison against a
  // competitor POS): the default view's Payment button (ut-docs#1252 —
  // opens the payment overlay the two tests above already drive) has its
  // OWN clipping failure mode neither of them can see: they both open the
  // overlay first, so they only ever assert on elements INSIDE it, which
  // is a <dialog> sized independently of `.pos-container`'s grid rows.
  // The trigger button itself — Hold Sale + Payment, `.tender-default-
  // footer` — lives in the always-visible default view and is exactly as
  // exposed to `.tender`'s own height budget as the pre-#1252 pay-grid
  // was. Two real regressions were caught live before this test existed:
  // the button clipped by a few px at 1024x600 even with an empty till,
  // and a growing held-sales strip (persistent DB state, not a transient)
  // pushed it further down until it was off-screen entirely at 3 held
  // sales — worse than the original always-visible-pay-grid bug this
  // column's whole redesign was meant to fix. Fixed with a `.tender-
  // scroll` wrapper (the growable part — scan row + held strip — scrolls
  // in its own region, same split `.basket-scroll`/`.totals` already use)
  // plus a small `@media (max-height)` row-split adjustment for the
  // genuine base-case budget shortfall that remained. This test holds
  // both fixes to account: no scroll, and no held-sales count regresses
  // it.
  test('the default-view Payment button is never clipped at 1024x600, with or without held sales', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const payBtn = page.getByTestId('payment-open');
    const hitTest = () =>
      payBtn.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const x = r.left + r.width / 2;
        const y = r.bottom - 2;
        if (y > window.innerHeight || y < 0) return false;
        const at = document.elementFromPoint(x, y);
        return !!at && (at === el || el.contains(at));
      });

    expect(await hitTest(), 'Payment button must be unclipped with an empty till, no scroll').toBe(true);

    // Hold three sales in a row — the held-sales strip is persistent DB
    // state (grows with real use, not a transient edge case) and is what
    // actually broke this before the fix. Deliberately NOT scrolling or
    // waiting for anything past each hold to settle, matching how an
    // operator would actually stack up parked orders.
    const codes = ['5000000000012', '5000000000029', '5000000000012'];
    for (let i = 0; i < codes.length; i++) {
      await page.locator('input[name="code"]').first().fill(codes[i]);
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
        page.locator('.scan-row button[type=submit]').click(),
      ]);
      await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
      const modal = page.locator('#hold-modal');
      await expect(modal).toBeVisible();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
        modal.locator('button[type=submit]').click(),
      ]);
      await expect(modal).toBeHidden();

      expect(
        await hitTest(),
        `Payment button must stay unclipped with ${i + 1} held sale(s), no scroll`,
      ).toBe(true);
    }
    assertClean();
  });

  // ut-docs#1327, 2026-08-30: the 900px-width stacked tablet tier (basket/
  // tender/products in one column — `.pos-container`'s own
  // `@media (max-width: 900px)` block) clipped the Payment button too,
  // independently of the 1024x600 kiosk-floor bug the tests above guard —
  // this one is width-driven, not height-driven (confirmed: broken at
  // every height from 800px down at this width, before the fix). Fixed as
  // a side effect of shrinking `.tender-default-footer` (row layout, base
  // .btn size instead of the old 4.2rem/1.15rem giant) for the product-
  // owner's compactness pass, not touched deliberately — this test pins
  // it so it can't silently regress next time this row's sizing changes.
  test('the default-view Payment button is not clipped at the 900px-width stacked tablet tier (ut-docs#1327)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 850, height: 700 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const payBtn = page.getByTestId('payment-open');
    const hitTest = () =>
      payBtn.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const x = r.left + r.width / 2;
        const y = r.bottom - 2;
        if (y > window.innerHeight || y < 0) return false;
        const at = document.elementFromPoint(x, y);
        return !!at && (at === el || el.contains(at));
      });

    expect(await hitTest(), 'Payment button must be unclipped at 850x700 with an empty till').toBe(true);

    await page.locator('input[name="code"]').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
    const modal = page.locator('#hold-modal');
    await expect(modal).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      modal.locator('button[type=submit]').click(),
    ]);
    await expect(modal).toBeHidden();

    expect(await hitTest(), 'Payment button must stay unclipped at 850x700 with a held sale present').toBe(true);
    assertClean();
  });
});
