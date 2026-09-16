import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2285: a long-press (~500ms hold, cancelled by >10px movement) or
// a right-click/contextmenu on a sell-screen tile opens a small tile-
// actions sheet (#tile-sheet) instead of adding the item to the basket:
// Move earlier/later (within the tile's own category), Remove from the
// quick buttons, Edit in catalog.
//
// HONESTY NOTE (per the `ux` skill's touch-sensitive-change rule, same
// convention as designer-reorder-1221.spec.ts's own note): the hold
// gesture below is driven via Playwright's synthetic mouse pointer
// (page.mouse), which Chromium's own input pipeline turns into real
// PointerEvents — not real touch hardware. Real-touch verification on the
// pilot tablet is the local lane's job, same split this codebase already
// draws elsewhere (see e.g. printer-test hardware notes in memory).

// Per-run suffix: cleanupItems() DEACTIVATES the fixture items, and a
// deactivated item no longer renders a .catalog-row, so a second run of
// this spec against the same still-running till (a local re-run after a
// failure — CI boots a fresh DB per run) could never re-seed under the
// same SKU: the import updates the deactivated row in place and seedItems
// then times out looking for a row that isn't there. Fresh names/SKUs/
// barcodes per run sidestep that entirely.
const RUN = Date.now().toString(36).toUpperCase();
const CAT = `Sheet2285 Cat ${RUN}`;
const ITEM_A = { name: `Sheet2285 Item A ${RUN}`, sku: `SHEET2285A${RUN}`, barcode: `SHEET2285BC-A-${RUN}`, category: CAT };
const ITEM_B = { name: `Sheet2285 Item B ${RUN}`, sku: `SHEET2285B${RUN}`, barcode: `SHEET2285BC-B-${RUN}`, category: CAT };
type Item = typeof ITEM_A;

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Mirrors category-switch-stale-tile-add-1433.spec.ts's own seedItems: a
// plain catalog import only creates `items` rows (BuildCategoryGroups
// never sees those directly, since the sale-screen grid groups SHORTCUT
// buttons, not catalog items), so each item is ALSO added as a sell-screen
// shortcut via /api/buttons/add so it actually renders as a tile.
async function seedItems(page: Page, items: Item[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: 'import-2285.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(csvFor(items)),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    const id = (await row.first().getAttribute('data-id'))!;
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id, label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
}

async function cleanupItems(page: Page, items: Item[]) {
  for (const it of items) {
    await page.request.post('/api/buttons/remove', { form: { code: it.barcode } });
  }
  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

// A real long-press: pointerdown, hold past app.js's 500ms threshold, then
// release without moving — the sheet-opening path.
async function longPress(tile: Locator) {
  const page = tile.page();
  const box = (await tile.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

test.describe('Sell-screen tile long-press sheet (ut-docs#2285)', () => {
  test('long-press opens the sheet; move/edit/remove all work end to end', async ({ page }) => {
    // Step (2c) below deliberately drives /ui/pos/tile-sheet to a 404;
    // htmx logs its own console.error for any non-2xx it handles, so that
    // one expected line is exempted — every other console error still
    // fails the test (same shape as ut-docs#916's extraExempt precedent).
    const assertClean = watchConsole(page, /Response Status Error Code 404 from \/ui\/pos\/tile-sheet/);
    await seedItems(page, [ITEM_A, ITEM_B]);

    try {
      await page.goto('/');
      const tileA = page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`);
      const tileB = page.locator(`.btn-tile[data-name="${ITEM_B.name}"]`);
      await expect(tileA).toBeVisible();
      await expect(tileB).toBeVisible();

      // (1) A plain click still adds to the basket normally — the
      // long-press machinery must never swallow an ordinary tap.
      await tileA.click();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await expect(page.locator('.basket .line-name').first()).toHaveText(ITEM_A.name);

      // (2) A real long-press on a DIFFERENT tile opens the sheet instead
      // of adding it — the basket line count from (1) stays untouched.
      await longPress(tileB);
      await expect(page.locator('#tile-sheet')).toBeVisible();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await page.keyboard.press('Escape');
      await expect(page.locator('#tile-sheet')).toBeHidden();

      // (2b) A LONG hold — sheet opens at ~500ms, the operator keeps the
      // finger down reading it, releases at ~1.8s. The trailing click is
      // released well after the sheet opened, so the swallow window must
      // be armed from the RELEASE, not from the open (independent review
      // finding M1, 2026-09-16: reproduced live as "sheet open AND item
      // added"). Deliberately on a tile whose centre is NOT under the
      // open sheet: on a covered tile the release lands on the sheet and
      // no tile click fires at all, which would pass even with the bug.
      await longPress(tileB);
      const sheetBox = (await page.locator('#tile-sheet').boundingBox())!;
      await page.keyboard.press('Escape');
      await expect(page.locator('#tile-sheet')).toBeHidden();
      const uncovered = await page.locator('.btn-tile[data-code]').evaluateAll((els, box) => {
        for (const el of els) {
          const r = (el as HTMLElement).getBoundingClientRect();
          const cx = r.x + r.width / 2, cy = r.y + r.height / 2;
          const inside = cx >= box.x && cx <= box.x + box.width && cy >= box.y && cy <= box.y + box.height;
          if (!inside && r.width > 0) return (el as HTMLElement).dataset.code!;
        }
        return null;
      }, sheetBox);
      expect(uncovered, 'need a tile whose centre is outside the open sheet').not.toBeNull();
      const uncoveredTile = page.locator(`.btn-tile[data-code="${uncovered}"]`);
      const ubox = (await uncoveredTile.boundingBox())!;
      await page.mouse.move(ubox.x + ubox.width / 2, ubox.y + ubox.height / 2);
      await page.mouse.down();
      await page.waitForTimeout(1800);
      await page.mouse.up();
      await expect(page.locator('#tile-sheet')).toBeVisible();
      await page.waitForTimeout(300); // give a leaked click time to reach /api/pos/scan
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await page.keyboard.press('Escape');
      await expect(page.locator('#tile-sheet')).toBeHidden();

      // (2c) A 404 for the sheet (the tile's code vanished — stale tab,
      // item deactivated elsewhere) must NOT open the dialog showing the
      // PREVIOUS tile's actions (independent review finding m2).
      await page.route('**/ui/pos/tile-sheet*', (route) => route.fulfill({ status: 404, body: 'gone' }));
      await longPress(tileB);
      await page.waitForTimeout(300);
      await expect(page.locator('#tile-sheet')).toBeHidden();
      await expect(page.locator('#tile-sheet')).toBeEmpty();
      await page.unroute('**/ui/pos/tile-sheet*');

      // (3) A press cancelled by movement opens neither the sheet nor adds
      // a basket line. Moves past the tile's own right edge — not just
      // app.js's own >10px cancel threshold — so the eventual mouseup
      // lands outside the tile entirely; the "no basket line" half of
      // this assertion would otherwise depend on exactly how wide this
      // one tile happens to render at 1024x600, not on anything app.js
      // itself controls.
      const boxB = (await tileB.boundingBox())!;
      await page.mouse.move(boxB.x + boxB.width / 2, boxB.y + boxB.height / 2);
      await page.mouse.down();
      await page.mouse.move(boxB.x + boxB.width + 40, boxB.y + boxB.height / 2);
      await page.waitForTimeout(700);
      await page.mouse.up();
      await expect(page.locator('#tile-sheet')).toBeHidden();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);

      // (4) Move later on A (first of the pair, added first so it sorts
      // first) swaps it with B in the grid, persists across a reload,
      // then Move earlier restores it. Queried by data-code across the
      // WHOLE page (not just a visible panel): the default "All" tab
      // (ut-docs#2212) already makes every category's tiles visible at
      // once, and DOM order reflects the server's own sort_order
      // regardless of which tab happens to be active.
      const codesInOrder = () =>
        page
          .locator(`.btn-tile[data-code^="SHEET2285BC-"][data-code$="-${RUN}"]`)
          .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code));
      await expect.poll(codesInOrder).toEqual([ITEM_A.barcode, ITEM_B.barcode]);

      await longPress(tileA);
      await expect(page.locator('#tile-sheet')).toBeVisible();
      const moveLater = page.locator('[data-testid="tile-sheet-move-later"]');
      await expect(moveLater).toBeEnabled();
      const moveResponse = page.waitForResponse(
        (r) => r.url().includes('/api/buttons/move') && r.request().method() === 'POST',
      );
      await moveLater.click();
      await moveResponse;
      await expect.poll(codesInOrder).toEqual([ITEM_B.barcode, ITEM_A.barcode]);
      // The sheet stays open (re-rendered in place, hx-target="#tile-sheet"),
      // now showing A's fresh edge state — last of the pair, move-later
      // disabled.
      await expect(page.locator('#tile-sheet')).toBeVisible();
      await expect(page.locator('[data-testid="tile-sheet-move-later"]')).toBeDisabled();
      await page.keyboard.press('Escape');

      await page.reload();
      await expect.poll(codesInOrder).toEqual([ITEM_B.barcode, ITEM_A.barcode]);

      // (4b) Moving from INSIDE a category tab keeps that tab selected:
      // the move's buttons-changed refresh outerHTML-swaps the whole
      // .products root, which used to rebuild its Alpine state on the
      // default All tab (independent review finding M3, 2026-09-16 —
      // the operator lost exactly the view showing where the tile went).
      const catTab = page.locator('.products-finder [role="tab"]', { hasText: CAT });
      await catTab.click();
      await expect(catTab).toHaveAttribute('aria-selected', 'true');

      // Restore with Move earlier, so this spec leaves shared server state
      // as it found it (same convention as designer-reorder-1221.spec.ts's
      // own move-back step).
      await longPress(page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`));
      const moveEarlier = page.locator('[data-testid="tile-sheet-move-earlier"]');
      const restoreResponse = page.waitForResponse(
        (r) => r.url().includes('/api/buttons/move') && r.request().method() === 'POST',
      );
      await moveEarlier.click();
      await restoreResponse;
      await expect.poll(codesInOrder).toEqual([ITEM_A.barcode, ITEM_B.barcode]);
      // (4b, continued) the refreshed grid is still on the category tab.
      await expect(page.locator('.products-finder [role="tab"]', { hasText: CAT })).toHaveAttribute('aria-selected', 'true');
      await page.keyboard.press('Escape');
      await expect(page.locator('#tile-sheet')).toBeHidden();
      await page.locator('#cat-tab-all').click();

      // (5) Edit → URL carries item/return, opens the catalog item dialog;
      // closing it navigates back to the sale screen.
      await longPress(page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`));
      const editLink = page.locator('[data-testid="tile-sheet-edit"]');
      await expect(editLink).toBeVisible();
      await editLink.click();
      await expect(page).toHaveURL(/\/catalog\?item=[^&]+&return=(%2F|\/)$/);
      await expect(page.locator('#item-form-modal')).toBeVisible();
      await page.locator('#item-form-close-btn').click();
      await expect(page).toHaveURL(/\/$/);

      // (6) Remove — LAST, and only on this spec's OWN fixture tile, never
      // real demo data: count tiles, long-press, accept the confirm
      // dialog, assert the tile is gone and the sheet closed; then restore
      // state by re-adding via /api/buttons/add with the SAME code/label/
      // itemId the removed tile's own data-code/data-item-id carried (the
      // runner boots a fresh DB per run anyway, but this keeps this file's
      // own state honest if it's ever re-run against a live server).
      await expect(page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`)).toBeVisible();
      const beforeCount = await page.locator('.btn-tile[data-code]').count();
      const tileAgain = page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`);
      const removedItemId = await tileAgain.getAttribute('data-item-id');
      const removedCode = await tileAgain.getAttribute('data-code');
      await longPress(tileAgain);
      await expect(page.locator('#tile-sheet')).toBeVisible();
      page.once('dialog', (d) => d.accept());
      const removeResponse = page.waitForResponse((r) => r.url().includes('/api/buttons/remove'));
      await page.locator('[data-testid="tile-sheet-remove"]').click();
      await removeResponse;
      await expect(page.locator('#tile-sheet')).toBeHidden();
      await expect(page.locator('.btn-tile[data-code]')).toHaveCount(beforeCount - 1);

      const readd = await page.request.post('/api/buttons/add', {
        form: { itemId: removedItemId ?? '', label: ITEM_A.name, code: removedCode ?? '' },
      });
      expect(readd.ok(), 're-add the removed tile').toBe(true);

      assertClean();
    } finally {
      await cleanupItems(page, [ITEM_A, ITEM_B]);
    }
  });
});
