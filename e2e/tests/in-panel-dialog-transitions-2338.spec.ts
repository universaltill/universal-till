import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2338: extends ADR-0097's motion vocabulary to two more surfaces —
// the /items rail's in-panel master-detail swap (#items-panel/#admin-panel/
// #manual-panel) and record-dialog.js's dialog open — deliberately as
// plain, opacity-only CSS keyframe animations (.ut-panel-fx and a dialog
// open ease),
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

// ut-docs#2944 / ADR-0122 §5: the record-dialog open ease (#2338's
// .ut-dialog-fx class) is replaced by base.html's shared popup zoom -- a
// transient Web Animations transform from the tapped button. What #2338
// pinned still holds: the close itself is never delayed. The zoom's own
// geometry is covered by popup-zoom-2944.spec.ts.

const transformAnims = (el: Element) => el.getAnimations()
  .filter((a) => ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((k) => 'transform' in k)).length;

test.describe('record-dialog open motion (#2338, now ADR-0122 §5)', () => {
  test('opening a standard record dialog (/categories) gets the shared zoom and no per-dialog class; close is instant', async ({ page }) => {
    const stopWatching = watchConsole(page);
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('/categories');

    // A slow zoom, so the check below provably sees it running.
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1000ms'));
    // The list header's New button opens the dialog in create mode
    // (ut-docs#2010's own reference spec uses the same header control).
    await page.locator('.list-header [data-record-dialog-open]').first().click();
    const dialog = page.locator('#category-dialog');
    await expect(dialog).toBeVisible();
    // The shared wrapper started the zoom right after .show(), on the
    // dialog itself; no per-dialog class is involved any more.
    expect(await dialog.evaluate(transformAnims)).toBe(1);
    expect(await dialog.evaluate((el) => el.className)).not.toContain('ut-dialog-fx');
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    // Transient (fill 'none'): nothing is left on the dialog afterwards.
    await expect.poll(() => dialog.evaluate((el) => getComputedStyle(el).transform)).toBe('none');

    // Close is instant -- never delayed for an exit animation (ADR-0097
    // rule 1): the native close has happened by the time Escape returns;
    // the shrink that may follow is inert and paint-only.
    await page.keyboard.press('Escape');
    const st = await dialog.evaluate((el) => ({ open: (el as HTMLDialogElement).open, closing: el.hasAttribute('data-ut-closing'), inert: el.hasAttribute('inert') }));
    expect(st.open).toBe(false);
    if (st.closing) expect(st.inert).toBe(true);
    await expect(dialog).toBeHidden();

    stopWatching();
  });

  test('under reduced motion, a dialog open or close creates no animation at all', async ({ page }) => {
    // Deliberately relies on the shared fixture's DEFAULT reduced-motion
    // state (fixtures.ts) — no emulateMedia override here.
    await page.goto('/categories');
    await page.locator('.list-header [data-record-dialog-open]').first().click();
    const dialog = page.locator('#category-dialog');
    await expect(dialog).toBeVisible();
    expect(await dialog.evaluate(transformAnims)).toBe(0);

    await page.keyboard.press('Escape');
    expect(await dialog.evaluate((el) => el.hasAttribute('data-ut-closing'))).toBe(false);
    await expect(dialog).toBeHidden();
  });
});
