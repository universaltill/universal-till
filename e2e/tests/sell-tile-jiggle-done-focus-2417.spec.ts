import { test, expect } from './fixtures';
import type { Locator } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2417: exit()'s focus fallback in app.js's jiggle-mode edit mode
// (ut-docs#2339) is meant to keep keyboard focus on the screen when Done
// is about to be display:none'd -- but the fallback queried
// '#buttons-grid .btn-tile[data-code]' unscoped, and #buttons-grid-all
// (the All tab's own dedicated grid, ut-docs#2294) renders FIRST inside
// #buttons-grid, before any category panel. So the query always resolved
// to a hidden All-grid tile, whose .focus() is a no-op, and keyboard focus
// fell all the way to <body> instead of landing on a real, visible tile.
// Found by independent review of #2402's fix
// (docs/code-reviews/2026-09-18-e2e-green-after-all-tab-2294.md); fixed by
// scoping the fallback query with the same inAllGrid() helper
// tileFor/badgeFor already use.
//
// Per-run suffix: same reasoning as sell-tile-jiggle-mode-2339.spec.ts's
// own note -- a fresh SKU/barcode per run avoids colliding with a
// deactivated leftover from a prior local re-run.
const RUN = Date.now().toString(36).toUpperCase();
const ITEM = { name: `Jiggle2417 Item ${RUN}`, sku: `JIG2417${RUN}` };

async function seedTile(page: import('@playwright/test').Page): Promise<void> {
  // categoryId: 'cat_food' (the demo seed's fixed, stable id -- see
  // internal/data/seeddata/demo_catalogue.sql) -- deliberately NOT left
  // uncategorized. An uncategorized quick button lands in
  // BuildCategoryGroups' synthetic "Uncategorized" bucket, which renders
  // LAST among the category tabs and carries no guaranteed room of its
  // own: since ut-docs#2498 the strip also shows Household/Produce (real
  // demo categories with items but no quick buttons, previously pruned
  // entirely), so at this test's default (unset) viewport the tab count
  // went from 4 (All/Food/Drinks/Uncategorized) to 6, tipping the strip's
  // own fit calculation (ut-docs#2307's applyCategoryOverflow(),
  // web/ui/partials/buttons.html) into overflow and hiding Uncategorized's
  // own tab button behind "...". Reproduced live: entering/exiting
  // jiggle-mode on a tile whose category's own .tab element is in that
  // hidden state made the test's own POST /api/buttons/remove cleanup
  // below hang for the full test timeout (never reaching the server --
  // confirmed by a concurrent curl to the same route succeeding in
  // milliseconds) -- a genuine but narrow browser-side interaction this
  // test has no reason to exercise. "Food" is one of the four demo
  // categories the strip always has comfortable room for (verified via
  // sale-screen-category-strip-overflow-2307.spec.ts's own case (b)), so
  // switching to it keeps this test's actual point (a real, visible
  // category-panel tile, as opposed to the hidden All-grid copy -- see
  // below) while no longer depending on the one bucket the strip can hide.
  const createResp = await page.request.post('/api/catalog/item', {
    form: { name: ITEM.name, price: '150', sku: ITEM.sku, categoryId: 'cat_food' },
  });
  expect(createResp.ok(), 'create catalog item').toBe(true);

  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${ITEM.name}"]`);
  const itemId = (await row.first().getAttribute('data-id'))!;

  const addResp = await page.request.post('/api/buttons/add', {
    form: { itemId, label: ITEM.name, code: ITEM.sku },
  });
  expect(addResp.ok(), 'add shortcut button').toBe(true);
}

// A real long-press: pointerdown, hold past app.js's 500ms threshold, then
// release without moving -- the mode-entering path. Mirrors
// sell-tile-jiggle-mode-2339.spec.ts's own identical helper (that file has
// no exported symbols to import from).
async function longPress(tile: Locator) {
  const page = tile.page();
  const box = (await tile.boundingBox())!;
  const c = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

test.describe('Jiggle-mode Done focus fallback never falls to <body> (ut-docs#2417)', () => {
  test('keyboard-activated Done focuses a real visible tile, not the hidden All-grid copy', async ({ page }) => {
    const assertClean = watchConsole(page);
    await seedTile(page);
    try {
      await page.goto('/');
      // ut-docs#2294: All is the default-selected tab, and its own
      // #buttons-grid-all grid renders a SECOND, hidden copy of this same
      // tile -- switch to the item's own category tab so the tile under
      // `.products-tab-panel` (the one the fallback SHOULD focus) is
      // genuinely visible, not just DOM-present. This is exactly the setup
      // the bug needs: #buttons-grid-all still renders first inside
      // #buttons-grid regardless of which tab is active, which is why the
      // unscoped query kept resolving to it even here. "Food", not
      // Uncategorized -- see seedTile's own comment above.
      await page.getByRole('tab', { name: 'Food' }).click();
      const tile = page.locator(`.products-tab-panel .btn-tile[data-name="${ITEM.name}"]`);
      await expect(tile).toBeVisible();

      await longPress(tile);
      const grid = page.locator('#buttons-grid');
      await expect(grid).toHaveClass(/jiggle-mode/);

      // Tab to the Done button and activate it via keyboard -- the natural
      // keyboard path, and the one exit()'s "focus was inside the bar"
      // branch exists for: a real <button>, Enter dispatches a click the
      // same as a tap would.
      await page.locator('[data-testid="jiggle-done"]').focus();
      await page.keyboard.press('Enter');

      await expect(grid).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeHidden();
      // The fix: focus lands on A real, visible tile -- never <body>, and
      // never a hidden #buttons-grid-all tile (whose .focus() would be a
      // silent no-op, which is exactly how this bug's fallback failed).
      // The Uncategorized panel also holds demo-seeded tiles ahead of our
      // own in DOM order, so the fallback's *first* candidate need not be
      // OUR tile -- what matters is that it's a genuinely visible one in
      // the active category panel, not the hidden All-grid copy.
      const active = await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        return {
          tag: el?.tagName ?? null,
          isBtnTile: !!el?.classList.contains('btn-tile'),
          inProductsPanel: !!el?.closest('.products-tab-panel'),
          inAllGrid: !!el?.closest('#buttons-grid-all'),
          visible: !!el && el.getClientRects().length > 0,
        };
      });
      expect(active.tag, 'focus must not fall to <body>').not.toBe('BODY');
      expect(active.isBtnTile, 'focus must land on a .btn-tile').toBe(true);
      expect(active.inProductsPanel, 'focus must land inside the visible category panel').toBe(true);
      expect(active.inAllGrid, 'focus must never land inside the hidden All grid').toBe(false);
      expect(active.visible, 'the focused tile must actually be visible').toBe(true);

      assertClean();
    } finally {
      await page.request.post('/api/buttons/remove', { form: { code: ITEM.sku } });
    }
  });
});
