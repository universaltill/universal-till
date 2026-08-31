import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#251: regression coverage for two real bugs the independent review
// of ut-docs#81 caught LIVE in the Payments card's .fee-row -- fixed in
// docs/code-reviews/2026-08-01-settings-page-layout-fix.md, but only ever
// verified by ad-hoc manual Playwright driving during that review, never a
// committed spec:
//
// 1. Input value clipping at every viewport/UI-scale -- the number-input
//    track was too narrow for its own field's documented max (100.00%,
//    999.99 fixed).
// 2. The Save button becoming completely unreachable below ~430px -- the
//    row's fixed-rem tracks overflowed the ancestor `.card`'s
//    `overflow: hidden` with no scroll escape.
//
// Precedent for this shape: tender-panel-reachable.spec.ts (button
// reachability proved via a real, completing click) and
// ui-scale-basket.spec.ts (the ui_scale select-and-submit flow this file
// reuses verbatim). basket-no-horizontal-scroll-391.spec.ts is the sibling
// "value clipped inside its own input" assertion (scrollWidth vs
// clientWidth, 1px sub-pixel tolerance) this file mirrors for .fee-row
// specifically -- its own comment lists #251 among prior related cards.
//
// Cash/Card payment methods are seeded by 001_init.sql, so the Payments
// card -- and at least one .fee-row -- renders unconditionally on the
// default e2e till; no fixture setup needed.

async function firstFeeRow(page) {
  await page.goto('/settings');
  const row = page.locator('.fee-row').first();
  await expect(row, 'Payments card must render at least one .fee-row').toBeVisible();
  return row;
}

async function readClip(row) {
  return row.evaluate((el) => {
    const p = el.querySelector('input[name="percent"]') as HTMLInputElement;
    const f = el.querySelector('input[name="fixed"]') as HTMLInputElement;
    return {
      percent: { scrollWidth: p.scrollWidth, clientWidth: p.clientWidth },
      fixed: { scrollWidth: f.scrollWidth, clientWidth: f.clientWidth },
    };
  });
}

test.describe('settings Payments card .fee-row stays legible and reachable (ut-docs#251)', () => {
  test('percent/fixed inputs do not clip a realistic max value', async ({ page }) => {
    const assertClean = watchConsole(page);
    const row = await firstFeeRow(page);
    await row.locator('input[name="percent"]').fill('100.00');
    await row.locator('input[name="fixed"]').fill('999.99');

    const clip = await readClip(row);
    // 1px tolerance: same sub-pixel-rounding allowance
    // basket-no-horizontal-scroll-391.spec.ts documents for non-integer
    // --ui-scale values -- legitimately full-value, unlike a real clip.
    expect(
      clip.percent.scrollWidth,
      `percent input "100.00" must not clip (scrollWidth ${clip.percent.scrollWidth} vs clientWidth ${clip.percent.clientWidth})`,
    ).toBeLessThanOrEqual(clip.percent.clientWidth + 1);
    expect(
      clip.fixed.scrollWidth,
      `fixed input "999.99" must not clip (scrollWidth ${clip.fixed.scrollWidth} vs clientWidth ${clip.fixed.clientWidth})`,
    ).toBeLessThanOrEqual(clip.fixed.clientWidth + 1);
    assertClean();
  });

  test('Save button is reachable at a narrow (390px) viewport', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 390, height: 800 });
    const row = await firstFeeRow(page);
    const save = row.locator('button[type="submit"]');

    const cardBox = await row.evaluate((el) => el.closest('.card')!.getBoundingClientRect());
    const inside = (b: { x: number; width: number }) => b.x >= cardBox.left - 1 && b.x + b.width <= cardBox.right + 1;

    let saveBox = await save.boundingBox();
    if (!saveBox || !inside(saveBox)) {
      // Escape hatch: `.fee-row { overflow-x: auto }` -- the row itself
      // scrolls horizontally rather than the Save button ever being
      // clipped by the card's own `overflow: hidden`.
      await row.evaluate((el) => {
        el.scrollLeft = el.scrollWidth;
      });
      saveBox = await save.boundingBox();
    }
    expect(saveBox, 'Save button must have a real, measurable box').toBeTruthy();
    expect(inside(saveBox!), 'Save must be reachable inside its card, directly or via .fee-row scroll').toBe(true);

    // A real, completing click is the strongest proof of reachability --
    // same standard tender-panel-reachable.spec.ts holds itself to. It
    // fails if the button is present-but-unclickable in a way geometry
    // alone can't see. Something always renders into .fee-msg -- a save
    // confirmation, a range error, or an elevation prompt -- regardless of
    // whether this till happens to gate the save behind manager PIN
    // elevation, so this doesn't couple the assertion to that permission
    // state. Safe on the shared e2e server: the row's DOM values were never
    // filled in this test, so this re-saves whatever is already persisted
    // for this method -- genuinely idempotent, not just untested.
    await save.click();
    await expect(row.locator('.fee-msg')).not.toBeEmpty();
    assertClean();
  });

  test.describe('at ui_scale 2', () => {
    // ui_scale is a server-side setting shared by every spec on this e2e
    // suite's single, single-worker till server. Setting/restoring it via a
    // direct API request (not the UI select+submit flow) sidesteps two
    // things: the settings-page-layout-fix review's own confirmed bfcache
    // false-negative from reusing one page across a viewport change plus an
    // in-page ui-scale goto, and -- found while writing this spec -- the
    // restore button genuinely wasn't reliably clickable at the 390px
    // viewport this test itself sets. An afterEach restore (not a manual
    // call at the tail) runs unconditionally, pass or fail, mirroring
    // basket-no-horizontal-scroll-391.spec.ts's own fix for the same leak
    // (ut-docs#510: a manual restore never runs when an earlier expect()
    // throws).
    test.afterEach(async ({ page }) => {
      await page.request.post('/api/settings/ui-scale', { form: { scale: '1' } });
    });

    test('percent/fixed inputs do not clip at ui_scale 2 on a narrow viewport', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.request.post('/api/settings/ui-scale', { form: { scale: '2' } });
      await page.setViewportSize({ width: 390, height: 800 });
      const row = await firstFeeRow(page);
      await row.locator('input[name="percent"]').fill('100.00');
      await row.locator('input[name="fixed"]').fill('999.99');

      const clip = await readClip(row);
      expect(
        clip.percent.scrollWidth,
        `percent input must not clip at ui_scale 2 (scrollWidth ${clip.percent.scrollWidth} vs clientWidth ${clip.percent.clientWidth})`,
      ).toBeLessThanOrEqual(clip.percent.clientWidth + 1);
      expect(
        clip.fixed.scrollWidth,
        `fixed input must not clip at ui_scale 2 (scrollWidth ${clip.fixed.scrollWidth} vs clientWidth ${clip.fixed.clientWidth})`,
      ).toBeLessThanOrEqual(clip.fixed.clientWidth + 1);
      assertClean();
    });
  });

  // Bonus per ut-docs#251's acceptance criteria (not required): the two
  // .settings-wide cards (Backup table, All Settings table) use
  // `column-span: all` specifically so they don't fall back to an internal
  // horizontal scrollbar like a normal masonry column would.
  test('settings-wide cards render without their own internal horizontal scrollbar', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/settings');
    const cards = page.locator('.card.settings-wide');
    const count = await cards.count();
    expect(count, 'expected at least one .settings-wide card (Backup / All Settings tables)').toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      const box = await cards.nth(i).evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }));
      expect(
        box.scrollWidth,
        `settings-wide card #${i} must not scroll horizontally itself (scrollWidth ${box.scrollWidth} vs clientWidth ${box.clientWidth})`,
      ).toBeLessThanOrEqual(box.clientWidth);
    }
    assertClean();
  });
});
