import { test, expect } from './fixtures';
import { setOskMode, watchConsole, openNewItemForm } from './helpers';

// ut-docs#1998: `body.osk-padded` (and, via the same custom property,
// `.payment-overlay`/`.item-form-modal` — app.css) reserved a hardcoded
// 15.5rem for the on-screen keyboard (#osk, osk.js) — ~1.45rem short of the
// keyboard's real rendered height (288px measured at that build's 17px root
// font-size). Nothing was actually covered only because .catalog-form-body's
// own padding-block-end happened to absorb almost exactly the shortfall —
// by accident, not by design — so any future change to that padding, the
// root font-size, or the OSK's own height would silently start covering the
// last control in a form.
//
// Fix: osk.js's show() now measures #osk's own getBoundingClientRect().height
// and writes it to `--osk-reserved-height` on <html>; every CSS reservation
// reads that custom property instead of a hardcoded number. This spec pins
// two things: (1) the reserved value genuinely tracks the real keyboard, not
// a second hardcoded guess, and (2) the practical consequence — the last
// control in the item form stays a real, un-covered hit target — holds at
// both the kiosk floor and phone width, without relying on any container's
// own incidental bottom padding to make up a shortfall.
//
// (.payment-overlay's own reachability, including a genuine end-to-end
// click through Complete Sale, is already covered by
// payment-overlay-osk-1385.spec.ts and is unaffected in kind by this change
// — it reads the same custom property this spec pins here.)

test.describe('OSK reserved height tracks the keyboard\'s real height (ut-docs#1998)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('--osk-reserved-height equals #osk\'s own rendered height, not a hardcoded guess', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/catalog');
    await openNewItemForm(page);
    await page.locator('#item-description').click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();

    const m = await page.evaluate(() => {
      const osk = document.getElementById('osk')!;
      const real = osk.getBoundingClientRect().height;
      const reserved = parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue('--osk-reserved-height'),
      );
      // padding-block-end resolves to `padding-bottom` for this document's
      // horizontal-tb writing mode regardless of `dir` (RTL only mirrors
      // the inline axis, never the block axis) — this is the CSS side of
      // the fix actually CONSUMING the custom property above, not just
      // osk.js correctly computing and publishing it. Independent review
      // (ut-docs#1998) proved these two can drift apart silently: reverting
      // ONLY app.css back to a hardcoded 15.5rem while keeping the fixed
      // osk.js still passed every other assertion in this file, because at
      // some viewports the pre-existing shortfall is small enough to still
      // be absorbed by .catalog-form-body's own incidental padding — the
      // exact "correct by accident" failure mode ut-docs#1998 itself was
      // filed about. Checking the actual applied padding closes that gap.
      const bodyPadding = parseFloat(getComputedStyle(document.body).paddingBottom);
      return { real, reserved, bodyPadding };
    });
    expect(m.real, 'the OSK must actually be laid out (non-zero height) for this assertion to mean anything').toBeGreaterThan(0);
    // toBeCloseTo(..., 0) allows sub-pixel rounding only — a real drift
    // (the original bug was off by ~23px) fails this comfortably.
    expect(
      m.reserved,
      '--osk-reserved-height must track the OSK\'s actual rendered height',
    ).toBeCloseTo(m.real, 0);
    expect(
      m.bodyPadding,
      'body.osk-padded\'s actual applied padding-bottom must equal the OSK\'s real height — CSS must be reading --osk-reserved-height, not a stale hardcoded value',
    ).toBeCloseTo(m.real, 0);
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor' },
    { width: 360, height: 740, label: 'phone width' },
  ]) {
    test(`the last control in the item form is a real, un-covered hit target at ${vp.label} (${vp.width}x${vp.height})`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await setOskMode(page, 'on');
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/catalog');
      await openNewItemForm(page);

      // A plain text field — raises the full (5-row) layout, the same
      // layout every other letter locale renders, so this isn't scoped to
      // whichever locale the test happens to run under.
      await page.locator('#item-description').click();
      await expect(page.locator('#osk.osk-open')).toBeVisible();

      // Scroll the Details panel all the way down — the exact position the
      // original bug report measured the overlap at.
      const body = page.locator('.catalog-form-body');
      await body.evaluate((el) => { el.scrollTop = el.scrollHeight; });

      // #item-active (the Active checkbox) is the last control in the
      // Details tab (catalog.html).
      const last = page.locator('#item-active');
      await expect(last).toBeVisible();
      const hit = await last.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        // The checkbox's own <label> wrapper commonly receives the hit
        // instead of the <input> itself — either direction counts as a
        // real, reachable target.
        return !!at && (el.contains(at) || at.contains(el));
      });
      expect(
        hit,
        'the last control in the form must be a real hit-test target, not covered by the open keyboard',
      ).toBe(true);
      assertClean();
    });
  }
});
