import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#2024: at 360px the item form's tab strip (ut-docs#2000) scrolls
// as a single row, but had no visual hint that tabs sit off-screen. Fix
// (app.css + catalog.html): two sticky pseudo-elements
// (.tab-bar::before/::after, positioned via inset-inline-start/-end so the
// browser's own bidi resolution puts them on the right physical edge for
// both LTR and RTL) fade in/out via tabBarFade(), which toggles
// .tab-bar--fade-start/-end off real scrollLeft/scrollWidth arithmetic.
//
// The genuinely fragile part, worth locking in directly: Chromium/Firefox/
// Safari use scrollLeft 0→+max for LTR but the MIRRORED 0→−max for RTL, so
// a naive `scrollLeft > 0` check silently never fires under `dir="rtl"` —
// tabBarFade() uses Math.abs() specifically to be correct for both without
// a sign-specific branch. Assert on the ::before/::after computed opacity
// (the user-visible outcome), not just the class list, so a CSS rename
// that breaks the class-to-pseudo wiring would also fail this.

async function fadeOpacity(bar: import('@playwright/test').Locator) {
  return bar.evaluate((el) => ({
    start: getComputedStyle(el, '::before').opacity,
    end: getComputedStyle(el, '::after').opacity,
  }));
}

// The fade cross-fades over app.css's own `transition: opacity .15s ease`,
// so reading getComputedStyle() the instant a scroll/open happens can
// legitimately catch it mid-transition (e.g. "0.852523") — real behaviour,
// not a bug, but not what these assertions care about either. Poll to the
// settled value instead of asserting on a single immediate read.
async function expectFade(bar: import('@playwright/test').Locator, edge: 'start' | 'end', visible: boolean, msg: string) {
  await expect.poll(async () => (await fadeOpacity(bar))[edge], { message: msg }).toBe(visible ? '1' : '0');
}

test.describe('catalog item form tab strip scroll-shadow (ut-docs#2024)', () => {
  test('LTR: shows the end-fade at rest, flips to the start-fade once scrolled to the last tab', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    const bar = page.locator('.catalog-form-head .tab-bar');
    await expect(bar).toBeVisible();

    // At rest (scrollLeft 0): more tabs to the right, none hidden to the
    // left — end-fade visible, start-fade not.
    await expectFade(bar, 'start', false, 'no fade at the true start');
    await expectFade(bar, 'end', true, 'fade visible where more tabs are off-screen');

    await page.locator('#item-form-tab-keypad').click();
    await bar.evaluate((el) => { el.scrollLeft = el.scrollWidth; });
    await expectFade(bar, 'start', true, 'start-fade should appear once scrolled to the last tab');
    await expectFade(bar, 'end', false, 'no fade at the true end');

    assertClean();
  });

  test('RTL (fa): the same fade logic is correct under the negative scrollLeft convention', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await openNewItemForm(page);

    const bar = page.locator('.catalog-form-head .tab-bar');
    await expect(bar).toBeVisible();

    // At rest, RTL still starts at scrollLeft 0 (first tab visible,
    // reading-start) — same at-rest expectation as LTR.
    await expectFade(bar, 'start', false, 'no fade at the true start (RTL)');
    await expectFade(bar, 'end', true, 'fade visible where more tabs are off-screen (RTL)');

    // Scroll to the reading-end. RTL's scrollLeft goes NEGATIVE toward the
    // end (verified live: 0 → -(scrollWidth-clientWidth)) — this is
    // exactly the convention a naive positive-only check would miss.
    const scrolledLeft = await bar.evaluate((el) => {
      el.scrollLeft = -(el.scrollWidth - el.clientWidth);
      return el.scrollLeft;
    });
    expect(scrolledLeft, 'RTL scrollLeft must actually go negative here').toBeLessThan(0);

    await expectFade(bar, 'start', true, 'start-fade should appear once scrolled to the last tab (RTL)');
    await expectFade(bar, 'end', false, 'no fade at the true end (RTL)');

    assertClean();
  });

  test('kiosk floor (1024x600): no fade ever shows — the strip wraps instead of scrolling', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    const bar = page.locator('.catalog-form-head .tab-bar');
    await expect(bar).toBeVisible();

    // The fade rules (including the ::before/::after `content` that makes
    // them generate a box at all) live inside app.css's ≤700px media
    // query, so above that width the pseudo-elements don't exist —
    // asserting the CLASS (tabBarFade()'s actual output) is what's
    // meaningful here; a bare getComputedStyle(el,'::before').opacity
    // would misleadingly read '1' (CSS's initial value) for a
    // pseudo-element with no `content` at all, not a real fade.
    const classes = await bar.evaluate((el) => el.className);
    expect(classes, 'no fade class at the kiosk floor').not.toMatch(/tab-bar--fade-/);

    const noOverflow = await bar.evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
    expect(noOverflow, 'the strip should not overflow at 1024px (it wraps instead)').toBe(true);

    assertClean();
  });

  // ut-docs#2032: tabBarFade() used to only run on the tab bar's own
  // 'scroll' event plus once on open — a resize/rotation that crosses the
  // 700px breakpoint while the dialog stays open never touched either, so
  // the fade classes went stale until the next scroll or reopen. Fix wires
  // the same tabBarFade() to 'resize'/'orientationchange', guarded by
  // modal.open.
  test('resize while the dialog stays open recomputes the fade across the 700px breakpoint', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    const bar = page.locator('.catalog-form-head .tab-bar');
    await expect(bar).toBeVisible();

    // Kiosk floor: no fade class yet (same baseline as the test above).
    let classes = await bar.evaluate((el) => el.className);
    expect(classes, 'no fade class at the kiosk floor').not.toMatch(/tab-bar--fade-/);

    // Shrink below the breakpoint WITHOUT closing/reopening the dialog —
    // no scroll ever happens here, so only the new resize listener can be
    // what puts the end-fade back.
    await page.setViewportSize({ width: 360, height: 740 });
    await expectFade(bar, 'end', true, 'resize alone (no scroll, no reopen) should recompute the end-fade');
    await expectFade(bar, 'start', false, 'no fade at the true start after a resize');

    // Grow back past the breakpoint: the strip no longer overflows, so
    // both fade classes should clear again, still without a scroll/reopen.
    await page.setViewportSize({ width: 1024, height: 600 });
    classes = await bar.evaluate((el) => el.className);
    expect(classes, 'fade classes should clear once the strip stops overflowing').not.toMatch(/tab-bar--fade-/);

    assertClean();
  });

  // ut-docs#2032, independent review finding: the resize/orientationchange
  // fix above re-runs its whole containing <script> IIFE on every htmx
  // fragment swap of this "content" block (internal/pages/catalog/
  // handlers.go), which is exactly what the /items rail
  // (web/ui/partials/items_rail.html, hx-get hx-target="#items-panel", at
  // >=52rem viewport per app.css's .items-layout breakpoint) does on every
  // Catalog/Modifiers/etc. click — a naive `window.addEventListener` here
  // would register one more permanent listener per swap, since `window`
  // (unlike the tab-bar-scoped `scroll` listener) outlives the swapped-out
  // DOM. The real fix registers at most once per page load, guarded, and
  // resolves the live modal/tab-bar at event time rather than closing over
  // a specific render's detached elements.
  test('repeated htmx panel swaps via the /items rail never register more than one resize/orientationchange listener', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).__resizeListenerAdds = 0;
      (window as any).__orientListenerAdds = 0;
      const orig = window.addEventListener.bind(window);
      window.addEventListener = ((type: string, ...rest: any[]) => {
        if (type === 'resize') (window as any).__resizeListenerAdds++;
        if (type === 'orientationchange') (window as any).__orientListenerAdds++;
        return (orig as any)(type, ...rest);
      }) as typeof window.addEventListener;
    });
    // >=52rem (832px) so the rail swaps #items-panel in place via hx-get
    // rather than falling back to a full page navigation below that width.
    await page.setViewportSize({ width: 1024, height: 700 });
    await page.goto('/items');

    const counts = () => page.evaluate(() => ({
      resize: (window as any).__resizeListenerAdds,
      orient: (window as any).__orientListenerAdds,
    }));

    // Land on Catalog once and let the count settle (the /items page's own
    // initial render plus this first in-place click may each run the
    // catalog script's IIFE — what matters is that it stops growing from
    // here, not the exact starting number).
    await page.locator('.items-rail-list a[href="/catalog"]').click();
    await expect(page.locator('#item-form-add-btn')).toBeVisible();
    const baseline = await counts();
    expect(baseline.resize, 'at least one resize listener registered by now').toBeGreaterThanOrEqual(1);
    expect(baseline.orient, 'at least one orientationchange listener registered by now').toBeGreaterThanOrEqual(1);

    // Swap away and back three times via the rail (htmx fragment swaps,
    // never a full page reload) — the count must NOT grow further: each
    // additional swap re-runs the IIFE, and the leaking version would add
    // one more resize + one more orientationchange listener per round trip.
    for (let i = 0; i < 3; i++) {
      await page.locator('.items-rail-list a[href="/modifiers"]').click();
      await expect(page.locator('.items-rail-list a[href="/catalog"]')).toBeVisible();
      await page.locator('.items-rail-list a[href="/catalog"]').click();
      await expect(page.locator('#item-form-add-btn')).toBeVisible();
    }
    const afterRoundTrips = await counts();
    expect(afterRoundTrips.resize, 'resize listener count must not grow after repeated rail swaps').toBe(baseline.resize);
    expect(afterRoundTrips.orient, 'orientationchange listener count must not grow after repeated rail swaps').toBe(baseline.orient);

    // And the fix still actually works after all those swaps: open the
    // form, shrink below the breakpoint, confirm the fade still recomputes
    // live off the CURRENT (not some stale detached) modal/tab-bar.
    await openNewItemForm(page);
    const bar = page.locator('.catalog-form-head .tab-bar');
    await page.setViewportSize({ width: 360, height: 740 });
    await expectFade(bar, 'end', true, 'fade still recomputes correctly after repeated swaps');

    assertClean();
  });
});
