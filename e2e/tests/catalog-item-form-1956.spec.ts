import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#1956: product-owner feedback on the catalog item form — "the
// forms are so confusing". Five changes, all pinned here:
//   1. the add/edit dialog is genuinely full-screen;
//   2. the action bar (Save / Close / + New / icon-only Delete) is pinned
//      and never scrolls away;
//   3. the three <details> accordions (item image, print labels, keypad)
//      became tabs inside the form;
//   4. the variants/barcodes panel moved from the bottom of the page into
//      the form, as its own tab;
//   5. an icon-only delete sits in the bar — deactivate semantics, so an
//      item with trading history is preserved (the row disappears from the
//      list, same as the row's own ✕).
// Plus the kiosk floors: 1024×600 keeps the bar in one usable row; 360px
// wraps it without overlapping content or overflowing horizontally.

const TABS = ['details', 'variants', 'image', 'labels', 'keypad'] as const;

async function createItem(page: Page, name: string) {
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('1.50');
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
  await closeItemForm(page);
}

async function openRow(page: Page, name: string) {
  // A plain cell, not the row's centre — the row-click handler ignores
  // clicks that land on a `.btn`.
  await page.locator('#catalog-table .catalog-row', { hasText: name }).first().locator('td').first().click();
  await expect(page.locator('#item-form-modal')).toBeVisible();
}

test.describe('catalog item form (ut-docs#1956)', () => {

  // --- Regression tests for the three defects the independent review found
  // --- (F1/F2/F3). All three had the same cause: Alpine's x-show write
  // --- lands on the SECOND animation frame, and each of these acted on a
  // --- freshly-selected tab panel while it was still display:none. Each
  // --- test below fails against the pre-fix code for its own reason.

  test('F1: Save on another tab reports the empty required field instead of failing silently', async ({ page }) => {
    await page.goto('/catalog');
    await openNewItemForm(page);
    // Leave Name and Price empty, then go somewhere else and press Save.
    await page.locator('#item-form-tab-labels').click();
    await expect(page.locator('#item-form-panel-labels')).toBeVisible();
    await page.locator('#item-form-submit').click();
    // The form must come back to Details AND actually report the field —
    // pre-fix this switched tabs a frame later and told the operator
    // nothing at all: no bubble, no notice, no focus move.
    await expect(page.locator('#item-form-panel-details')).toBeVisible();
    await expect(page.locator('#item-name')).toBeFocused();
    // ...and nothing was saved.
    await expect(page.locator('#item-form-msg .pos-notice.success')).toHaveCount(0);
  });

  test('F2: reopening the dialog focuses the barcode field, never the delete button', async ({ page }) => {
    await page.goto('/catalog');
    await createItem(page, 'Focus Probe A');
    await createItem(page, 'Focus Probe B');

    // Leave a NON-Details tab selected, close, then open a different item.
    await openRow(page, 'Focus Probe A');
    await page.locator('#item-form-tab-keypad').click();
    await expect(page.locator('#item-form-panel-keypad')).toBeVisible();
    await closeItemForm(page);

    await openRow(page, 'Focus Probe B');
    // Pre-fix, modal.show() ran its focusing steps while the Details panel
    // was still display:none, so #item-barcode was not a valid autofocus
    // candidate and focus fell to the dialog's first focusable descendant —
    // the icon-only DELETE button. A barcode scanner ends every scan with
    // Enter, which would then raise the deactivate confirm.
    await expect(page.locator('#item-form-delete')).not.toBeFocused();
    await expect(page.locator('#item-barcode')).toBeFocused();
  });

  test('F3: opening the Keypad tab focuses its capture field', async ({ page }) => {
    await page.goto('/catalog');
    await createItem(page, 'Keypad Probe');
    await openRow(page, 'Keypad Probe');
    await page.locator('#item-form-tab-keypad').click();
    await expect(page.locator('#item-form-panel-keypad')).toBeVisible();
    // "Opening the Keypad tab IS the 'now press the physical key' moment" —
    // pre-fix the focus() call ran against a display:none panel and was a
    // no-op, so the operator's keypress went nowhere. This worked before
    // #1956 (the old <details> path focused on row-select), so it was a
    // functional regression, not merely an unimplemented nicety.
    await expect(page.locator('#keypad-capture')).toBeFocused();
  });

  test('F4: checkboxes in the variants panel keep their box beside their label', async ({ page }) => {
    await page.goto('/catalog');
    await createItem(page, 'Cascade Probe');
    await openRow(page, 'Cascade Probe');
    await page.locator('#item-form-tab-variants').click();
    await expect(page.locator('#item-form-panel-variants')).toBeVisible();
    // catalog_variants.html moved INSIDE .catalog-form, so
    // `.catalog-form label` (0,1,1) started beating the panel's own rules
    // and stacked every checkbox above its text, doubling each row's height.
    const probe = page.locator('#catalog-variants label.chip-add-primary').first();
    await expect(probe).toBeVisible();
    const m = await probe.evaluate((el) => {
      const box = el.querySelector('input')!.getBoundingClientRect();
      const label = el.getBoundingClientRect();
      return { dir: getComputedStyle(el).flexDirection, h: label.height, sameRow: box.top < label.bottom - box.height / 2 };
    });
    expect(m.dir, 'the variants panel must not inherit the item form\'s column-flex labels').not.toBe('column');
    expect(m.h, 'a checkbox row should stay one line tall').toBeLessThan(32);
    expect(m.sameRow, 'the checkbox must sit beside its text, not above it').toBe(true);
  });
  test('the dialog covers the full viewport', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await openNewItemForm(page);
    const box = await page.locator('#item-form-modal').boundingBox();
    const vp = page.viewportSize()!;
    expect(box).not.toBeNull();
    expect(box!.x).toBe(0);
    expect(box!.y).toBe(0);
    expect(Math.round(box!.width)).toBe(vp.width);
    expect(Math.round(box!.height)).toBe(vp.height);
    assertClean();
  });

  test('the action bar stays visible and clickable when the body is scrolled to the bottom', async ({ page }) => {
    const assertClean = watchConsole(page);
    // Short viewport so the Details panel is guaranteed taller than the body.
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/catalog');
    await openNewItemForm(page);
    const body = page.locator('.catalog-form-body');
    const scrollable = await body.evaluate((el) => el.scrollHeight > el.clientHeight);
    expect(scrollable, 'the Details panel must scroll inside .catalog-form-body at 1024x600').toBe(true);
    await body.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    for (const id of ['#item-form-submit', '#item-form-close-btn']) {
      const b = page.locator(id);
      await expect(b).toBeVisible();
      await expect(b).toBeInViewport({ ratio: 1 });
    }
    // The head really is pinned: its box is unchanged by the scroll.
    const head = await page.locator('.catalog-form-head').boundingBox();
    expect(head!.y).toBe(0);
    // And the Close button genuinely receives the click at that position.
    await page.locator('#item-form-close-btn').click();
    await expect(page.locator('#item-form-modal')).toBeHidden();
    assertClean();
  });

  test('switching tabs shows exactly that panel; Details is the default', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await openNewItemForm(page);
    await expect(page.locator('#item-form-tab-details')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#item-form-panel-details')).toBeVisible();
    // No accordion controls anywhere in the dialog.
    await expect(page.locator('#item-form-modal details, #item-form-modal summary')).toHaveCount(0);
    for (const t of TABS) {
      await page.locator(`#item-form-tab-${t}`).click();
      await expect(page.locator(`#item-form-tab-${t}`)).toHaveAttribute('aria-selected', 'true');
      await expect(page.locator(`#item-form-panel-${t}`)).toBeVisible();
      for (const other of TABS) {
        if (other === t) continue;
        await expect(page.locator(`#item-form-panel-${other}`)).toBeHidden();
        await expect(page.locator(`#item-form-tab-${other}`)).toHaveAttribute('aria-selected', 'false');
      }
    }
    // Create mode: the item does not exist yet, so the item-bound tabs say
    // what to do next instead of showing a dead panel.
    await page.locator('#item-form-tab-variants').click();
    await expect(page.locator('#item-form-panel-variants')).toContainText('Save this item first');
    await expect(page.locator('#item-form-panel-variants .catalog-detail-grid')).toHaveCount(0);
    await expect(page.locator('#item-form-delete')).toBeHidden();
    assertClean();
  });

  test('opening an existing item lands on Details, shows delete, and the Variants tab holds that item', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    const name = 'Form Probe ' + Date.now();
    await createItem(page, name);

    // Leave the dialog on a non-default tab first, so the reset is real.
    await openNewItemForm(page);
    await page.locator('#item-form-tab-labels').click();
    await expect(page.locator('#item-form-panel-labels')).toBeVisible();
    await closeItemForm(page);

    await openRow(page, name);
    await expect(page.locator('#item-form-tab-details')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#item-form-panel-details')).toBeVisible();
    await expect(page.locator('#item-form-delete')).toBeVisible();
    await expect(page.locator('#item-form-title')).toContainText(name);

    await page.locator('#item-form-tab-variants').click();
    const panel = page.locator('#item-form-panel-variants #catalog-variants');
    await expect(panel).toBeVisible();
    await expect(panel.locator('.catalog-detail-grid')).toBeVisible();
    await expect(panel).toContainText(name);
    // The panel's own duplicate item-name heading is not shown twice: the
    // dialog title already carries it. Visually hidden (1px clip box) for
    // assistive tech, not display:none — so check the box, not
    // toBeHidden(), which treats any non-empty box as visible.
    const h3 = panel.locator('.catalog-detail-title h3');
    await expect(h3).toHaveClass(/visually-hidden/);
    const h3Box = (await h3.boundingBox())!;
    expect(h3Box.width).toBeLessThanOrEqual(1);
    expect(h3Box.height).toBeLessThanOrEqual(1);
    // The panel is inside the dialog, not a page-level sibling.
    expect(await page.locator('#catalog-variants').evaluate((el) => !!el.closest('#item-form-modal'))).toBe(true);
    assertClean();
  });

  test('delete confirms, deactivates, closes the dialog and removes the row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    const name = 'Delete Probe ' + Date.now();
    await createItem(page, name);
    await openRow(page, name);

    const del = page.locator('#item-form-delete');
    await expect(del).toBeVisible();
    await expect(del).toHaveAttribute('aria-label', /.+/);
    await expect(del).toHaveAttribute('title', /.+/);
    // Touch target no smaller than the bar's other buttons.
    const dBox = (await del.boundingBox())!;
    const cBox = (await page.locator('#item-form-close-btn').boundingBox())!;
    expect(dBox.height).toBeGreaterThanOrEqual(cBox.height - 1);
    expect(dBox.width).toBeGreaterThanOrEqual(dBox.height - 1);

    // Deactivate, not erase: the confirm names the item and says the history is kept.
    let confirmText = '';
    page.once('dialog', (d) => { confirmText = d.message(); d.accept(); });
    await del.click();
    await expect.poll(() => confirmText).toContain(name);
    expect(confirmText.toLowerCase()).not.toContain('deactivate');
    expect(confirmText).toMatch(/history is kept/i);

    await expect(page.locator('#item-form-modal')).toBeHidden();
    await expect(page.locator('#catalog-table .catalog-row', { hasText: name })).toHaveCount(0);
    assertClean();
  });

  test('dismissing the delete confirm leaves the item and the dialog alone', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    const name = 'Keep Probe ' + Date.now();
    await createItem(page, name);
    await openRow(page, name);
    page.once('dialog', (d) => d.dismiss());
    await page.locator('#item-form-delete').click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await closeItemForm(page);
    await expect(page.locator('#catalog-table .catalog-row', { hasText: name })).toHaveCount(1);
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600', oneRow: true },
    { width: 360, height: 740, label: 'phone 360px', oneRow: false },
  ]) {
    test(`at ${vp.label} the action bar does not overlap content and nothing overflows horizontally`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/catalog');
      // The catalog LIST underneath is out of this card's scope (ut-docs#1951
      // owns it) and its wide items table already overflows 360px on its
      // own — so the assertion is that the dialog adds NO horizontal
      // overflow beyond whatever the bare page has, not that the bare page
      // is clean.
      const docOverflow = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      const baselineOverflow = await docOverflow();
      const name = 'Viewport Probe ' + Date.now();
      await createItem(page, name);
      await openRow(page, name);

      const head = (await page.locator('.catalog-form-head').boundingBox())!;
      const body = (await page.locator('.catalog-form-body').boundingBox())!;
      // Head above body, no overlap, both inside the viewport width.
      expect(body.y).toBeGreaterThanOrEqual(head.y + head.height - 0.5);
      expect(head.x + head.width).toBeLessThanOrEqual(vp.width + 0.5);
      expect(body.x + body.width).toBeLessThanOrEqual(vp.width + 0.5);

      // Every bar control is fully on screen and not overlapping another.
      const ids = ['#item-form-delete', '#item-form-reset', '#item-form-close-btn', '#item-form-submit'];
      const boxes: Array<{ id: string; x: number; y: number; w: number; h: number }> = [];
      for (const id of ids) {
        const b = (await page.locator(id).boundingBox())!;
        expect(b, id).not.toBeNull();
        expect(b.x, id).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width, id).toBeLessThanOrEqual(vp.width + 0.5);
        boxes.push({ id, x: b.x, y: b.y, w: b.width, h: b.height });
      }
      for (let i = 0; i < boxes.length; i++) {
        for (let j = i + 1; j < boxes.length; j++) {
          const a = boxes[i], b = boxes[j];
          const overlap = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
          expect(overlap, `${a.id} overlaps ${b.id}`).toBe(false);
        }
      }
      if (vp.oneRow) {
        const ys = new Set(boxes.map((b) => Math.round(b.y)));
        expect(ys.size, 'action bar buttons must share one row at the kiosk floor').toBe(1);
      }

      // No horizontal overflow inside the dialog (on every tab), and none
      // added to the page by opening it.
      for (const t of TABS) {
        await page.locator(`#item-form-tab-${t}`).click();
        await expect(page.locator(`#item-form-panel-${t}`)).toBeVisible();
        // ut-docs#1956 (independent review, F5): do NOT ask
        // .catalog-form-body for scrollWidth - clientWidth. It sets
        // overflow-x: hidden, and a hidden-overflow box always reports
        // scrollWidth === clientWidth — so that assertion returned 0 and
        // could never fail, however far the content ran past the edge.
        // Measure real geometry instead: which elements have their right
        // edge outside the body's own box. That is the check ut-docs#1967's
        // defect (Save Variant off-screen) would actually have tripped.
        const dlg = await page.evaluate(() => {
          const d = document.getElementById('item-form-modal')!;
          const b = d.querySelector('.catalog-form-body')! as HTMLElement;
          const bounds = b.getBoundingClientRect();
          const past: string[] = [];
          for (const el of Array.from(b.querySelectorAll<HTMLElement>('button, input, select, a'))) {
            const r = el.getBoundingClientRect();
            if (r.width === 0 && r.height === 0) continue;       // hidden
            if (el.closest('.variant-grid')) continue;            // owns an intentional scroller — ut-docs#1967
            if (r.right > bounds.right + 1) past.push(el.id || el.className || el.tagName);
          }
          return { dlg: d.scrollWidth - d.clientWidth, past };
        });
        expect(dlg.dlg, `dialog overflows horizontally on the ${t} tab`).toBeLessThanOrEqual(0);
        expect(dlg.past, `controls sit past the right edge of the form body on the ${t} tab`).toEqual([]);
      }
      expect(await docOverflow()).toBeLessThanOrEqual(baselineOverflow);
      assertClean();
    });
  }
});
