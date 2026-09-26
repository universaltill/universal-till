import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2338: extends ADR-0097's motion vocabulary to two more surfaces —
// the /items rail's in-panel master-detail swap (#items-panel/#admin-panel/
// #manual-panel) and record-dialog.js's dialog open — deliberately as
// plain, opacity-only CSS keyframe animations (.ut-panel-fx/.ut-dialog-fx),
// never the View Transition API and never a `transform`: an earlier draft
// of this card used `transform: translateX(...)` on the swapped panel and
// was caught in independent review before merge — the panel hosts
// `.record-dialog`/`.item-form-modal` (`position: fixed` descendants), and
// any non-`none` transform on an ancestor becomes their containing block,
// the exact hazard ADR-0097 rule 2 already names for `view-transition-name`
// on `<main>`. See internal/pages/transitions_test.go for the static
// source guards; this file proves the BEHAVIOUR in a real Chromium,
// mirroring page-transitions-2223.spec.ts's own split of concerns.
//
// Every test drives the panel/dialog with reduced motion turned OFF
// explicitly (the shared `page` fixture defaults every spec to a
// reduced-motion user, fixtures.ts) — the reduced-motion coverage itself
// is the LAST test in this file, which relies on that same default.

// The in-panel swap half of #2338 (.ut-panel-fx) was replaced by ADR-0122's
// tree-pane zoom -- see tree-pane-zoom-2943.spec.ts.

test.describe('record-dialog open ease (#2338)', () => {
  test('opening a standard record dialog (/categories) gets an opacity+scale ease, and close() is instant', async ({ page }) => {
    const stopWatching = watchConsole(page);
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('/categories');

    let sawOpenFx = false;
    await page.exposeFunction('__reportDialogFx', () => { sawOpenFx = true; });
    await page.locator('#category-dialog').evaluate((el) => {
      new MutationObserver(() => {
        if (el.classList.contains('ut-dialog-fx')) (window as any).__reportDialogFx();
      }).observe(el, { attributes: true, attributeFilter: ['class'] });
    });

    // The list header's New button opens the dialog in create mode
    // (ut-docs#2010's own reference spec uses the same header control).
    await page.locator('.list-header [data-record-dialog-open]').first().click();
    await expect(page.locator('#category-dialog')).toBeVisible();
    await page.waitForTimeout(200);
    expect(sawOpenFx).toBe(true);

    // Close is instant — never delayed for an exit animation (ADR-0097
    // rule 1: motion must never add latency on a cashier-facing surface).
    const before = Date.now();
    await page.keyboard.press('Escape');
    await expect(page.locator('#category-dialog')).toBeHidden();
    expect(Date.now() - before).toBeLessThan(300);

    stopWatching();
  });

  test('under reduced motion, a dialog open never gets the ease class at all', async ({ page }) => {
    // Deliberately relies on the shared fixture's DEFAULT reduced-motion
    // state (fixtures.ts) — no emulateMedia override here.
    await page.goto('/categories');

    let sawOpenFx = false;
    await page.exposeFunction('__reportDialogFxReduced', () => { sawOpenFx = true; });
    await page.locator('#category-dialog').evaluate((el) => {
      new MutationObserver(() => {
        if (el.classList.contains('ut-dialog-fx')) (window as any).__reportDialogFxReduced();
      }).observe(el, { attributes: true, attributeFilter: ['class'] });
    });

    await page.locator('.list-header [data-record-dialog-open]').first().click();
    await expect(page.locator('#category-dialog')).toBeVisible();
    await page.waitForTimeout(200);
    expect(sawOpenFx).toBe(false);

    await page.keyboard.press('Escape');
    await expect(page.locator('#category-dialog')).toBeHidden();
  });
});
