import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#1989: at the 1024x600 kiosk floor, the Variants tab's trailing
// content (the customization-groups summary + "Manage customization
// groups" button, ut-docs#1957) was reported as "barely reachable" below
// the variants grid, with a screenshot showing the dialog cutting off
// around that area.
//
// Re-verified against current `main` (ut-docs#1956/#1957 already merged):
// `.catalog-form-body` is a real flex/overflow-y:auto scroller (the same
// mechanism ut-docs#1956's own "action bar stays visible ... scrolled to
// the bottom" test already proves for the Details tab), so the Variants
// tab's trailing content scrolls into view exactly the same way — this was
// not independently covered by any existing spec. #1956's own test only
// drives the Details tab; #1967's test only covers the variant grid's
// *horizontal* scroller. Neither exercises scrolling `.catalog-form-body`
// all the way down on the Variants tab specifically.
//
// This spec locks that in as a real regression test: with several variants
// pushing the panel well past the 600px-tall viewport, the summary block
// and its button must still end up fully on-screen and genuinely
// clickable (real elementFromPoint hit-test, not just present in the DOM)
// after scrolling `.catalog-form-body` to its end.
test.describe('catalog item form: modifier-groups summary stays reachable (ut-docs#1989)', () => {
  test('at 1024x600, "Manage customization groups" is reachable after scrolling the Variants tab', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');

    const name = 'Modifier Reach Probe ' + Date.now();
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.50');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    await page.locator('.catalog-row', { hasText: name }).first().click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-form-tab-variants').click();
    await expect(page.locator('#item-form-panel-variants')).toBeVisible();

    // Push the panel taller than the 600px-tall viewport so the summary
    // block genuinely starts below the fold, same shape as ut-docs#1967's
    // own "taller than the body" setup.
    for (let i = 0; i < 4; i++) {
      await page.locator('input[form="vf-new"][name="name"]').fill('Variant ' + i);
      await page.locator('.vg-row.vg-new input.variant-price-major').fill('1.00');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
        page.locator('button[form="vf-new"][type=submit]').click(),
      ]);
      await page.locator('#item-form-tab-variants').click();
      await expect(page.locator('#item-form-panel-variants')).toBeVisible();
    }

    const body = page.locator('.catalog-form-body');
    const scrollable = await body.evaluate((el) => el.scrollHeight > el.clientHeight);
    expect(scrollable, 'the Variants panel must be taller than .catalog-form-body at 1024x600 for this test to mean anything').toBe(true);

    await body.evaluate((el) => { el.scrollTop = el.scrollHeight; });

    const btn = page.locator('#manage-modifiers-btn');
    await expect(btn).toBeVisible();
    await expect(btn).toBeInViewport({ ratio: 1 });
    const hit = await btn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, '"Manage customization groups" must be the real hit-test target, not just present in the DOM').toBe(true);

    // Genuinely clickable, not just geometrically on-screen: the click
    // must actually open the nested dialog.
    await btn.click();
    await expect(page.locator('#modifier-groups-modal')).toBeVisible();
    assertClean();
  });
});
