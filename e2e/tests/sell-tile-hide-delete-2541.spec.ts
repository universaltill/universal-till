import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2541: every active catalog item is a sell-screen quick button by
// default (no manual "add button" step), and jiggle edit mode gives each
// tile three actions: edit (unchanged), remove (trash, after a confirm
// naming it) and hide (eye-off, bottom-end corner -- off the purchase page
// only; it still sells via search/scan and comes back from the Designer's
// "Hidden from sell screen" list). ut-docs#2698: the trash badge removes the
// tile from the quick buttons only (the item stays in the catalog), and a
// hidden tile stays greyed in its spot while editing -- see
// sell-tile-hidden-greyed-2698.spec.ts for those flows in full.
//
// Same honesty note as sell-tile-jiggle-mode-2339.spec.ts: gestures are
// Playwright's synthetic mouse pointer in Chromium, not real touch on the
// pilot till's WebKitGTK kiosk.

const RUN = Date.now().toString(36).toUpperCase();
const CAT = `Hide2541 Cat ${RUN}`;
const A = { name: `Hide2541 Item A ${RUN}`, sku: `HID2541A${RUN}`, barcode: `HID2541BC-A-${RUN}` };
const B = { name: `Hide2541 Item B ${RUN}`, sku: `HID2541B${RUN}`, barcode: `HID2541BC-B-${RUN}` };

async function importItems(page: Page) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-2541-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(
      'Name,SKU,Barcode,Price,Category,In stock\n' +
        [A, B].map((it) => `${it.name},${it.sku},${it.barcode},1.00,${CAT},1`).join('\n'),
    ),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
}

async function itemId(page: Page, name: string): Promise<string | null> {
  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  if ((await row.count()) === 0) return null;
  return row.first().getAttribute('data-id');
}

function tile(page: Page, name: string): Locator {
  return page.locator(`.products-tab-panel .btn-tile[data-name="${name}"]`);
}

// The tile's wrapper cell (holds the badges as siblings of the tile).
function cellOf(page: Page, name: string): Locator {
  return page.locator('.products-tab-panel .tile-cell', { has: page.locator(`.btn-tile[data-name="${name}"]`) });
}

async function enterJiggle(page: Page, t: Locator) {
  await t.click({ button: 'right' });
  await expect(page.locator('#buttons-grid.jiggle-mode')).toHaveCount(1);
}

test.describe('every item is a quick button; hide / unhide / remove (ut-docs#2541, #2698)', () => {
  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    for (const it of [A, B]) {
      const id = await itemId(page, it.name);
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
    }
    await page.close();
  });

  test('imported items appear as tiles; hide, search, unhide, remove', async ({ page }) => {
    const assertClean = watchConsole(page);
    await importItems(page);

    // (1) No manual step: both imported items are tiles in their category.
    await page.goto('/');
    await page.getByRole('tab', { name: CAT }).click();
    await expect(tile(page, A.name)).toBeVisible();
    await expect(tile(page, B.name)).toBeVisible();

    // (2) Jiggle mode shows three badges on a tile, none overlapping.
    await enterJiggle(page, tile(page, A.name));
    const cell = cellOf(page, A.name);
    const edit = cell.getByTestId('tile-badge-edit');
    const del = cell.getByTestId('tile-badge-remove');
    const hide = cell.getByTestId('tile-badge-hide');
    for (const b of [edit, del, hide]) await expect(b).toBeVisible();
    const [eb, db, hb] = await Promise.all([edit.boundingBox(), del.boundingBox(), hide.boundingBox()]);
    const overlap = (p: any, q: any) =>
      p.x < q.x + q.width && q.x < p.x + p.width && p.y < q.y + q.height && q.y < p.y + p.height;
    expect(overlap(eb, db) || overlap(eb, hb) || overlap(db, hb), 'badges must not overlap').toBe(false);
    // Hide sits at the bottom-end corner: below the delete badge, same (end) side.
    expect(hb!.y).toBeGreaterThan(db!.y + db!.height / 2);
    expect(Math.abs(hb!.x - db!.x)).toBeLessThan(db!.width);
    await page.screenshot({ path: test.info().outputPath('jiggle-badges.png') });

    // (3) Hide: no confirm; ut-docs#2698: greyed in place while editing,
    // out of sight once editing ends.
    await Promise.all([page.waitForResponse((r) => r.url().includes('/api/buttons/hide') && r.ok()), hide.click()]);
    await expect(cellOf(page, A.name)).toHaveClass(/tile--hidden/);
    await expect(tile(page, A.name)).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(tile(page, A.name)).toBeHidden();

    // (4) A hidden item still sells via search.
    await page.reload();
    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill(A.name.slice(0, 20));
    const result = page.locator('#search-results .btn-tile', { hasText: A.name });
    await expect(result).toBeVisible({ timeout: 5000 });
    await result.click();
    await expect(page.locator('#basket')).toContainText(A.name);
    const line = page.locator('#basket-lines tr', { hasText: A.name });
    await line.locator('.shrinkage-remove-toggle').click();
    await line.locator('.shrinkage-sheet-actions .btn').first().click();
    await expect(page.locator('#basket')).not.toContainText(A.name);

    // (5) The Designer lists it as hidden; Unhide brings the tile back.
    const idA = (await itemId(page, A.name))!;
    await page.goto('/designer');
    const hiddenRow = page.getByTestId(`designer-hidden-item-${idA}`);
    await expect(hiddenRow).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/unhide') && r.ok()),
      page.getByTestId(`designer-hidden-unhide-${idA}`).click(),
    ]);
    await expect(hiddenRow).toHaveCount(0);
    await page.goto('/');
    await page.getByRole('tab', { name: CAT }).click();
    await expect(tile(page, A.name)).toBeVisible();

    // (6) Remove (trash): cancelling the confirm (which names the item)
    // changes nothing.
    await enterJiggle(page, tile(page, B.name));
    const delB = cellOf(page, B.name).getByTestId('tile-badge-remove');
    let confirmText = '';
    page.once('dialog', (d) => {
      confirmText = d.message();
      d.dismiss();
    });
    await delB.click();
    expect(confirmText).toContain(B.name);
    await page.waitForTimeout(300);
    await expect(tile(page, B.name)).toBeVisible();

    // Accepting it removes the tile -- gone from the grid even while editing
    // -- but NOT the item: its catalog row is still there (ut-docs#2698).
    page.once('dialog', (d) => d.accept());
    await Promise.all([page.waitForResponse((r) => r.url().includes('/api/buttons/remove-from-grid') && r.ok()), delB.click()]);
    await expect(tile(page, B.name)).toHaveCount(0);
    expect(await itemId(page, B.name)).not.toBeNull();

    assertClean();
  });
});
