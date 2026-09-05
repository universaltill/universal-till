import { test, expect } from './fixtures';

// ut-docs#1175: follow-up to ut-docs#1126. self_order.html, self_order_shop.html
// and order_tracking.html are standalone documents (own <head>, never extend
// web/ui/layouts/base.html) that still hardcoded the OLD, pre-ut-docs#161
// fixed-px `uiscalepx` mechanism instead of the viewport-responsive
// `--ui-scale` + app.css fluid clamp that base.html/login.html/setup.html
// already use. Same test technique as login.spec.ts's #1126 coverage: a
// single test driving BOTH viewports per page, asserting the two root
// font-size readings actually DIFFER from each other — asserting only
// "!= 16" at each size independently would still pass for a broken fix that
// hardcodes any OTHER constant (e.g. `--ui-scale` wired to a literal instead
// of the template value).
test.describe('self-order and order-tracking viewport scaling (ut-docs#1175)', () => {
  const KIOSK = { width: 1024, height: 600 };
  const WAVESHARE = { width: 1920, height: 1200 };

  test("the self-order welcome screen's root scales with the viewport, not a fixed size", async ({ browser }) => {
    const measure = async (viewport: { width: number; height: number }) => {
      const ctx = await browser.newContext({ hasTouch: true, viewport });
      const p = await ctx.newPage();
      try {
        await p.goto('/self-order');
        const rootFontSize = parseFloat(await p.evaluate(() => getComputedStyle(document.documentElement).fontSize));
        // .selforder-start ("Tap to start") is always present — no catalog
        // or basket state needed, unlike the shop grid's item tiles.
        const box = await p.locator('.selforder-start').boundingBox();
        return { rootFontSize, startHeight: box?.height ?? 0 };
      } finally {
        await ctx.close();
      }
    };

    const kiosk = await measure(KIOSK);
    const waveshare = await measure(WAVESHARE);

    expect(kiosk.rootFontSize, 'root font-size at 1024x600 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size at 1920x1200 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size must actually respond to viewport size, not be a fixed value at both').toBeGreaterThan(kiosk.rootFontSize);

    expect(kiosk.startHeight, '"Tap to start" at 1024x600 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
    expect(waveshare.startHeight, '"Tap to start" at 1920x1200 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
  });

  test("the self-order shop screen's root scales with the viewport, not a fixed size", async ({ browser }) => {
    const measure = async (viewport: { width: number; height: number }) => {
      const ctx = await browser.newContext({ hasTouch: true, viewport });
      const p = await ctx.newPage();
      try {
        await p.goto('/self-order/shop');
        const rootFontSize = parseFloat(await p.evaluate(() => getComputedStyle(document.documentElement).fontSize));
        // The header "← Back" link is always present regardless of catalog
        // state, unlike the hx-loaded item grid.
        const box = await p.locator('.selforder-shop-header a.btn.secondary').boundingBox();
        return { rootFontSize, backHeight: box?.height ?? 0 };
      } finally {
        await ctx.close();
      }
    };

    const kiosk = await measure(KIOSK);
    const waveshare = await measure(WAVESHARE);

    expect(kiosk.rootFontSize, 'root font-size at 1024x600 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size at 1920x1200 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size must actually respond to viewport size, not be a fixed value at both').toBeGreaterThan(kiosk.rootFontSize);

    expect(kiosk.backHeight, '"Back" link at 1024x600 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
    expect(waveshare.backHeight, '"Back" link at 1920x1200 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
  });

  // order_tracking.html is NOT shop kiosk hardware — it renders on a
  // customer's OWN PHONE after they scan the self-order confirmation QR
  // (internal/pages/order_tracking.go), so there is no fixed target device
  // the way there is for the two kiosk screens above. A bogus token hits the
  // NotFound branch, which still renders the same full document (same
  // <html> style attribute) without needing a real sale/receipt fixture —
  // "not found" and "expired" are deliberately indistinguishable per that
  // handler's own comment, so this exercises the identical shell a real
  // tracking link would.
  test("the order-tracking screen's root scales with the viewport, not a fixed size", async ({ browser }) => {
    const measure = async (viewport: { width: number; height: number }) => {
      const ctx = await browser.newContext({ viewport });
      const p = await ctx.newPage();
      try {
        await p.goto('/o/e2e-1175-bogus-token');
        await expect(p.locator('body')).toBeVisible();
        const rootFontSize = parseFloat(await p.evaluate(() => getComputedStyle(document.documentElement).fontSize));
        return { rootFontSize };
      } finally {
        await ctx.close();
      }
    };

    const phone = await measure({ width: 375, height: 667 });
    const large = await measure(WAVESHARE);

    expect(phone.rootFontSize, 'root font-size at 375x667 must not be the old fixed 16px').not.toBe(16);
    expect(large.rootFontSize, 'root font-size at 1920x1200 must not be the old fixed 16px').not.toBe(16);
    expect(large.rootFontSize, 'root font-size must actually respond to viewport size, not be a fixed value at both').toBeGreaterThan(phone.rootFontSize);
  });
});
