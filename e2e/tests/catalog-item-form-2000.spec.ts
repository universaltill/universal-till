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

  // ut-docs#6138: the 360x640 phone (the common small Android) was
  // measured at 28.2% in create mode but 32.3% in edit mode, where the
  // extra +New button no longer fits beside Close/Save and wraps the action
  // group onto a second row.
  for (const mode of ['create', 'edit'] as const) {
    test(`the pinned head is < 30% of a 360x640 viewport in ${mode} mode (ut-docs#6138)`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 360, height: 640 });
      await page.goto('/catalog');
      if (mode === 'create') {
        await openNewItemForm(page);
      } else {
        await page.locator('[id^=catalog-row-]').first().click();
        await expect(page.locator('#item-form-modal')).toBeVisible();
        await expect(page.locator('#item-form-delete')).toBeVisible();
      }
      const head = (await page.locator('.catalog-form-head').boundingBox())!;
      const fraction = head.height / 640;
      expect(fraction, `${mode}: head is ${(fraction * 100).toFixed(1)}% (${head.height}px of 640)`).toBeLessThan(0.30);
      if (mode === 'edit') {
        // +New stays reachable by its accessible name.
        await expect(page.getByRole('button', { name: /new/i }).first()).toBeVisible();
        // Independent review (UX gate): fitting the head by shrinking the
        // title left the h3 ~45px wide ("Edi…"), hiding which item is open.
        // The title must show in full for a normal-length name...
        const title = await page.locator('#item-form-title').evaluate((el) => ({
          sw: el.scrollWidth, cw: el.clientWidth, text: el.textContent || '',
        }));
        expect(title.sw, `edit title "${title.text}" is truncated (${title.sw}px in ${title.cw}px)`).toBeLessThanOrEqual(title.cw);
        // ...and Delete joins +New/Close/Save on ONE button row.
        const tops = new Set<number>();
        for (const id of ['#item-form-delete', '#item-form-reset', '#item-form-close-btn', '#item-form-submit']) {
          tops.add(Math.round((await page.locator(id).boundingBox())!.y));
        }
        expect(tops.size, 'delete, +New, Close and Save must share one row at 360px').toBe(1);
      }
      assertClean();
    });
  }

  // ut-docs#6138: the items list scrolled sideways by ~6px at 360px in fa.
  for (const [route, lang] of [['/items', 'fa'], ['/items', 'ar'], ['/catalog', 'fa'], ['/catalog', 'ar']]) {
    test(`${route} does not scroll horizontally at 360px in ${lang} (ut-docs#6138)`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 640 });
      await page.goto(`${route}?lang=${lang}`);
      await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      const m = await page.evaluate(() => {
        const se = document.scrollingElement!;
        const offenders: string[] = [];
        for (const el of Array.from(document.body.querySelectorAll('*'))) {
          const r = el.getBoundingClientRect();
          if (r.width > 0 && (r.right > se.clientWidth + 0.5 || r.left < -0.5)) {
            offenders.push(`${el.tagName.toLowerCase()}#${el.id}.${(el as HTMLElement).className}`.slice(0, 80));
          }
        }
        return { sw: se.scrollWidth, cw: se.clientWidth, offenders: offenders.slice(0, 6) };
      });
      expect(m.sw, `scrollWidth ${m.sw} vs clientWidth ${m.cw}; offenders: ${m.offenders.join(' | ')}`).toBe(m.cw);
    });
  }
});
