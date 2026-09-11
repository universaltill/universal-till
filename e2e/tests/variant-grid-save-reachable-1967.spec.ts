import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#1967: the variants grid's own horizontal scroller (armed under
// 1300px viewport width — app.css's `.vg-cols { min-inline-size: 54rem }`
// block) could carry the Save Variant / Save button off past the right
// edge while the *page* itself had no horizontal overflow at all — only
// the grid did, which is exactly why this survived the suite's existing
// page-level overflow assertions (see e.g. basket-no-horizontal-scroll-391
// .spec.ts, which asserts on `document.documentElement.scrollWidth`, not
// on any element inside a nested scroller). Fixed by sticking the grid's
// own rightmost column — the commit action, in every row: the header's
// empty cell, each existing variant's own Save, and the add-variant row's
// Save Variant — to the scroll container's trailing edge (app.css,
// `.vg-cols > :last-child`).
//
// This drives the real geometry against the real viewport (bounding box +
// a real elementFromPoint hit-test), never page-level overflow, per the
// card's own acceptance criteria.

async function createProbeItemAndOpenVariants(page: import('@playwright/test').Page, name: string) {
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('1.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

  const row = page.locator('.catalog-row', { hasText: name });
  await row.click();
  // ut-docs#1956: the variants panel is no longer a full-width card BELOW
  // the item list — it is the item form's own Variants tab, inside the
  // full-screen dialog the row click opens. So this navigates to the tab
  // instead of closing the dialog and scrolling the page down to find the
  // panel. Nothing about what this file MEASURES changes: the assertions
  // below are the same real-geometry bounding-box + elementFromPoint
  // hit-tests against the same viewport, and they still fail if the grid's
  // trailing sticky column (app.css, `.vg-cols > :last-child`) regresses.
  await expect(page.locator('#item-form-modal')).toBeVisible();
  await page.locator('#item-form-tab-variants').click();
  await expect(page.locator('#item-form-panel-variants')).toBeVisible();
  await expect(page.locator('#catalog-variants')).toBeVisible();

  // The panel is taller than the form body at narrow widths, so the
  // add-variant row starts below the fold — the same "ordinary, expected
  // vertical scroll" the original page-level version of this helper handled
  // with scrollIntoViewIfNeeded(). Done by hand, and VERTICALLY ONLY, on
  // purpose: Playwright's scrollIntoViewIfNeeded() scrolls every ancestor
  // scroller including the grid's own horizontal one, which would scroll the
  // Save button into view sideways and mask the exact defect this file
  // exists to catch. geometry() asserts that horizontal scroller is still at
  // 0 afterwards, so the measurements below can't be reached by cheating.
  await page.evaluate(() => {
    const body = document.querySelector('.catalog-form-body') as HTMLElement | null;
    const btn = document.querySelector('button[form="vf-new"][type=submit]') as HTMLElement | null;
    if (!body || !btn) return;
    const delta = btn.getBoundingClientRect().top - body.getBoundingClientRect().top;
    body.scrollTop += delta - body.clientHeight / 2;
  });
}

async function geometry(page: import('@playwright/test').Page, selector: string) {
  return page.locator(selector).evaluate((el) => {
    const r = el.getBoundingClientRect();
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    // ut-docs#1956: the grid's own horizontal scroller must still be at its
    // start — otherwise "reachable" would only mean "reachable after the
    // sideways drag this card exists to remove."
    const scroller = el.closest('.variant-grid') as HTMLElement | null;
    return {
      withinViewport: r.width > 0 && r.left >= 0 && r.right <= window.innerWidth,
      hit: !!at && (at === el || el.contains(at)),
      gridScrollLeft: scroller ? Math.round(Math.abs(scroller.scrollLeft)) : 0,
    };
  });
}

test.describe('variants grid Save/Save Variant stays reachable without horizontal scroll (ut-docs#1967)', () => {
  // The two viewports the card's own acceptance criteria names: the kiosk
  // floor and the pilot tablet. Both must need NO horizontal scrolling of
  // any container to reach the commit action.
  for (const vp of [
    { width: 1024, height: 600 },
    { width: 1280, height: 800 },
  ]) {
    test(`Save Variant is fully within the viewport at ${vp.width}x${vp.height}, no scrolling needed`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto('/catalog');
      await createProbeItemAndOpenVariants(page, `Save Reach Probe ${vp.width} ` + Date.now());

      // Deliberately not scrolling `.variant-grid` first — the whole point
      // of the fix is that the button is already reachable at rest.
      const scrollLeft = await page.locator('.variant-grid').evaluate((el) => el.scrollLeft);
      expect(scrollLeft, 'must be measurable in its resting (unscrolled) position').toBe(0);

      const result = await geometry(page, 'button[form="vf-new"][type=submit]');
      expect(result.withinViewport, `Save Variant's bounding box must be fully within the ${vp.width}px viewport`).toBe(true);
      expect(result.hit, 'Save Variant must be a real hit-test target, not occluded or off-screen').toBe(true);
      expect(result.gridScrollLeft, 'reached without scrolling the grid sideways').toBe(0);

      assertClean();
    });
  }

  // 360px phone tier: the grid may still scroll (acceptance criteria says
  // so explicitly), but the commit action itself must not require it.
  test('Save Variant remains reachable at 360px phone width without scrolling the grid', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    await createProbeItemAndOpenVariants(page, 'Save Reach Probe 360 ' + Date.now());

    const result = await geometry(page, 'button[form="vf-new"][type=submit]');
    expect(result.withinViewport, "Save Variant's bounding box must be within the 360px viewport without scrolling").toBe(true);
    expect(result.hit, 'Save Variant must be a real hit-test target at 360px').toBe(true);
    expect(result.gridScrollLeft, 'reached without scrolling the grid sideways').toBe(0);

    assertClean();
  });

  // The fix sticks the whole rightmost column, not just the add-row's own
  // button — an already-saved variant's per-row Save sits in the identical
  // grid position and was exactly as unreachable before this fix.
  test("an EXISTING variant row's own Save button also stays reachable at 1024x600", async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');
    await createProbeItemAndOpenVariants(page, 'Save Reach Existing-Row Probe ' + Date.now());

    await page.locator('input[form="vf-new"][name="name"]').fill('330ml');
    await page.locator('input[form="vf-new"].variant-price-major').fill('2.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
      page.locator('button[form="vf-new"][type=submit]').click(),
    ]);
    const existingRow = page.locator('.vg-row:not(.vg-new)');
    await expect(existingRow).toBeVisible();

    // Direct child, not just any `[type=submit]` under the row — the
    // barcode chip-add form's "＋" button lives under this same row too
    // (nested inside .chip-row), but isn't the commit action this card is
    // about.
    const result = await geometry(page, '.vg-row:not(.vg-new) > button[type=submit]');
    expect(result.withinViewport, "the existing row's Save button must be fully within the viewport").toBe(true);
    expect(result.hit, "the existing row's Save button must be a real hit-test target").toBe(true);
    expect(result.gridScrollLeft, 'reached without scrolling the grid sideways').toBe(0);

    assertClean();
  });

  // Added in review. The whole fix rests on `inset-inline-end` being a
  // LOGICAL property (this repo's RTL rule: never left/right), so under
  // dir="rtl" it must resolve to left:0 and pin the commit action to the
  // visual trailing edge — the mirror image of the LTR cases above. A
  // physical `right: 0` would pass every test above and strand the button
  // off the RTL edge, which is exactly the regression worth locking down.
  test('Save Variant stays reachable under an RTL locale at 1024x600', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await createProbeItemAndOpenVariants(page, 'Save Reach RTL Probe ' + Date.now());

    const btn = page.locator('button[form="vf-new"][type=submit]');
    // The logical property really did resolve the RTL way round, rather
    // than the assertions below passing for some unrelated reason.
    await expect(btn).toHaveCSS('left', '0px');

    const result = await geometry(page, 'button[form="vf-new"][type=submit]');
    expect(result.withinViewport, "Save Variant's bounding box must be within the viewport under RTL").toBe(true);
    expect(result.hit, 'Save Variant must be a real hit-test target under RTL').toBe(true);
    expect(result.gridScrollLeft, 'reached without scrolling the grid sideways').toBe(0);

    assertClean();
  });
});
