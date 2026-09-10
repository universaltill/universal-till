import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { openNewItemForm, closeItemForm, watchConsole } from './helpers';

// Same helper as catalog-item-form-1956.spec.ts's own openRow (not exported
// from helpers.ts, so duplicated here rather than reaching into another
// spec file) — ut-docs#1951's card grid made the whole card the click
// target (no inner buttons left to avoid landing on).
async function openRow(page: Page, name: string) {
  await page.locator('#catalog-table .catalog-row', { hasText: name }).first().click();
  await expect(page.locator('#item-form-modal')).toBeVisible();
}

// ut-docs#1997: `.catalog-form label` (0,1,1) beats `.field-checks` (0,1,0)
// on flex-direction, stacking every `.field-checks` checkbox above its own
// text and stretching it to the full width of the form — the same cascade
// collision ut-docs#1956's own F4 fix already patched for the variants
// panel's `.chip-add-primary`, one level down. `.field-checks` is used two
// shapes in this codebase: a <div> wrapping several plain <label> children
// (isWeighed/stockUntracked/isActive on the Details tab), and a class on a
// single standalone <label> itself ("Plain code" on Details,
// "Set as primary" on the Keypad tab) — both were broken; the bug report
// named only the two most visually dramatic (full-width) instances, but
// this spec pins all of them, the same geometry-based shape as F4's own
// test (checkbox top within the label's own line box), not just the
// computed flex-direction.

async function assertCheckboxBesideText(page: Page, labelSelector: string) {
  const label = page.locator(labelSelector);
  await expect(label).toBeVisible();
  const m = await label.evaluate((el) => {
    const box = el.querySelector('input')!.getBoundingClientRect();
    const l = el.getBoundingClientRect();
    return { dir: getComputedStyle(el).flexDirection, h: l.height, sameRow: box.top < l.bottom - box.height / 2 };
  });
  expect(m.dir, `${labelSelector} must not be column-flexed`).not.toBe('column');
  expect(m.h, `${labelSelector} should stay one line tall`).toBeLessThan(32);
  expect(m.sameRow, `${labelSelector}'s checkbox must sit beside its text, not above it`).toBe(true);
}

test.describe('catalog item form: .field-checks checkboxes stay beside their text (ut-docs#1997)', () => {
  test('the Details tab: a standalone label.field-checks ("Plain code") and .field-checks-div-nested labels (Weighed/Stock/Active)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await openNewItemForm(page);

    // "Plain code" — <label class="field-checks"> itself, the case
    // .field-checks label's own descendant selector never reached.
    await assertCheckboxBesideText(page, 'label:has(input[name="forcePlainBarcode"])');

    // Weighed/Stock-untracked/Active — plain <label>s nested inside a
    // <div class="field-checks">; the div-wrapped shape.
    await assertCheckboxBesideText(page, 'label:has(#item-weighed)');
    await assertCheckboxBesideText(page, 'label:has(#item-stock-untracked)');
    await assertCheckboxBesideText(page, 'label:has(#item-active)');

    await closeItemForm(page);
    assertClean();
  });

  test('the Keypad tab: "Set as primary" (label.field-checks) stays beside its text', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await openNewItemForm(page);
    await page.locator('#item-name').fill('Field Checks Probe');
    await page.locator('#item-price').fill('1.50');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await closeItemForm(page);

    await openRow(page, 'Field Checks Probe');
    await page.locator('#item-form-tab-keypad').click();
    await expect(page.locator('#item-form-panel-keypad')).toBeVisible();
    await assertCheckboxBesideText(page, 'label:has(#keypad-primary)');

    await closeItemForm(page);
    assertClean();
  });

  test('at 1024x600 and 360px, nothing else in .catalog-form regresses', async ({ page }) => {
    const assertClean = watchConsole(page);
    for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
      await page.setViewportSize(vp);
      await page.goto('/catalog');
      await openNewItemForm(page);
      // A plain (non-.field-checks) label must keep its OWN column layout —
      // the fix's :not(.field-checks):not(.field-checks label) exclusion
      // must not accidentally widen past the two .field-checks shapes.
      const name = await page.locator('label:has(#item-name)').evaluate((el) => getComputedStyle(el).flexDirection);
      expect(name, `label:has(#item-name) at ${vp.width}x${vp.height} must stay column-flexed like every other plain field`).toBe('column');
      await assertCheckboxBesideText(page, 'label:has(input[name="forcePlainBarcode"])');
      await closeItemForm(page);
    }
    assertClean();
  });
});
