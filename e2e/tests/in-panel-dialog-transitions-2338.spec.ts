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

test.describe('in-panel swap ease (#2338)', () => {
  test('an /items rail click gives #items-panel an opacity-only ease, never a transform', async ({ page }) => {
    const stopWatching = watchConsole(page);
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('/items');
    await expect(page.locator('#items-panel')).toBeVisible();

    // /categories is a real, different rail destination from the default
    // /catalog landing section.
    const captured: { transform: string; opacityAtAdd: number }[] = [];
    await page.exposeFunction('__reportPanelFx', (transform: string, opacity: string) => {
      captured.push({ transform, opacityAtAdd: parseFloat(opacity) });
    });
    await page.locator('#items-panel').evaluate((el) => {
      new MutationObserver(() => {
        if (el.classList.contains('ut-panel-fx')) {
          const cs = getComputedStyle(el);
          (window as any).__reportPanelFx(cs.transform, cs.opacity);
        }
      }).observe(el, { attributes: true, attributeFilter: ['class'] });
    });

    await page.locator('#items-rail a[href="/categories"]').click();
    await page.waitForTimeout(250);

    expect(captured.length).toBeGreaterThan(0);
    for (const c of captured) {
      // 'none' (no transform at all) is the only acceptable value — this is
      // the actual regression guard: a transform here re-creates the
      // fixed-descendant containing-block hazard ADR-0097 rule 2 exists to
      // prevent, whatever CSS property causes it.
      expect(c.transform).toBe('none');
      // Never a flash to blank (ADR-0097 rule 5): opacity must never read
      // as fully transparent while the class is active.
      expect(c.opacityAtAdd).toBeGreaterThan(0);
    }

    stopWatching();
  });

  test('#admin-panel gets the same treatment as #items-panel', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('/admin');
    const panel = page.locator('#admin-panel');
    if ((await panel.count()) === 0) test.skip(true, 'no #admin-panel on this build');
    await expect(panel).toBeVisible();

    let sawClass = false;
    await page.exposeFunction('__reportAdminFx', () => { sawClass = true; });
    await panel.evaluate((el) => {
      new MutationObserver(() => {
        if (el.classList.contains('ut-panel-fx')) (window as any).__reportAdminFx();
      }).observe(el, { attributes: true, attributeFilter: ['class'] });
    });

    const railLink = page.locator('a[hx-target="#admin-panel"]').first();
    if ((await railLink.count()) === 0) test.skip(true, 'no admin rail link to click');
    await railLink.click();
    await page.waitForTimeout(250);
    expect(sawClass).toBe(true);
  });
});

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
