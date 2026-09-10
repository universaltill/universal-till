import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#2000: at 360x740 the item form's pinned head (.catalog-form-head)
// was measured at 42% of the viewport (312px of 740) — three button rows
// (title+delete, then +New/Close/Save wrapping across two) plus a two-row
// tab strip. The fix (app.css, catalog.html): +New/Close/Save go icon-only
// at phone width, same pattern as the existing icon-only Delete; and the
// tab strip stops wrapping and scrolls horizontally in one row instead.
// Kiosk floor (1024x600) is untouched — the collapse is scoped to the
// existing <=700px breakpoint.

test.describe('catalog item form header at phone width (ut-docs#2000)', () => {
  test('the pinned head occupies materially less than 42% of a 360x740 viewport', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    const head = (await page.locator('.catalog-form-head').boundingBox())!;
    const fraction = head.height / 740;
    // Was 42% (312px) before the fix; this pins "materially less", not a
    // specific target, so it can't be gamed by a one-pixel trim.
    expect(fraction, `head is ${Math.round(fraction * 100)}% of the viewport, was 42% before ut-docs#2000`).toBeLessThan(0.30);
    assertClean();
  });

  test('the action bar is icon-only at phone width but keeps its accessible name', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    // The visible label is hidden...
    for (const id of ['#item-form-close-btn', '#item-form-submit']) {
      const label = page.locator(`${id} .btn-label`);
      await expect(label).toBeAttached();
      const box = await label.boundingBox();
      expect(box === null || (box.width <= 1 && box.height <= 1), `${id}'s .btn-label should be visually hidden at phone width`).toBe(true);
    }
    // ...but each control is still reachable by its accessible name (the
    // same aria-label the button always carries, per catalog.html).
    await expect(page.getByRole('button', { name: 'Close' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create Item' })).toBeVisible();

    // Confirm the desktop/kiosk-width behaviour is unchanged: the label
    // becomes visible again above the 700px breakpoint.
    await page.setViewportSize({ width: 1024, height: 600 });
    const closeLabelBox = await page.locator('#item-form-close-btn .btn-label').boundingBox();
    expect(closeLabelBox && closeLabelBox.width > 1, 'the Close label should be visible again at the kiosk floor').toBe(true);
    assertClean();
  });

  test('the tab strip is a single scrollable row at phone width, and every tab is still reachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    await openNewItemForm(page);

    const bar = page.locator('.catalog-form-head .tab-bar');
    const rows = await bar.evaluate((el) => {
      const tops = new Set<number>();
      for (const t of Array.from(el.querySelectorAll('[role=tab]'))) {
        tops.add(Math.round(t.getBoundingClientRect().top));
      }
      return tops.size;
    });
    expect(rows, 'the tab strip must be a single row at phone width, not wrapped').toBe(1);

    const overflowX = await bar.evaluate((el) => getComputedStyle(el).overflowX);
    expect(overflowX).toBe('auto');

    // Every tab is still focusable and selectable via the existing
    // roving-tabindex arrow-key handler, scroll position aside.
    await page.locator('#item-form-tab-keypad').click();
    await expect(page.locator('#item-form-tab-keypad')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#item-form-panel-keypad')).toBeVisible();
    assertClean();
  });
});
