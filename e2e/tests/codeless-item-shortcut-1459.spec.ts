import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1459: a catalog item imported with neither a barcode nor a SKU
// (the common shape for loose/unlabelled real-café items -- 144 of 229 rows
// on the reference café catalog CSV) could be found by the Designer's search but
// could never actually be added as a sale-screen button: ButtonStore.Add
// rejected an empty "code" outright with a 400, and AddVals's barcode->SKU
// fallback (ut-docs#1220) has nothing left to fall back to once both are
// blank. Drives the real reported flow end to end: import a codeless-item
// CSV row, add it as a button from the Designer's search (not a raw
// /api/buttons/add POST -- that would only prove the Go layer, not the
// actual operator flow), then confirm the resulting tile resolves on the
// real sale screen at the right price.
test('a catalog item with neither barcode nor SKU can be added as a button and rings up on the sale screen', async ({
  page,
}) => {
  const assertClean = watchConsole(page);
  const stamp = Date.now();
  const name = `Codeless1459 Item ${stamp}`;
  const category = `Codeless1459 Cat ${stamp}`;

  // Step 1 (the card's own repro): import a CSV row with a Name and Price
  // but empty SKU and Barcode columns.
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-1459-${stamp}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(`Name,SKU,Barcode,Price,Category,In stock\n${name},,,2.50,${category},1`),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  // Confirm it actually landed with neither identifier -- the catalog row
  // must show no "Barcodes:"/SKU summary, otherwise this isn't the
  // codeless case the card describes.
  await page.goto('/catalog');
  const row = page.locator('.catalog-row', { hasText: name });
  await expect(row).toBeVisible();
  await expect(row).not.toContainText('Barcodes:');

  // Step 2: add it as a button from the Designer's search -- the real
  // operator flow the card's repro describes, not a raw API POST.
  await page.goto('/designer');
  const search = page.locator('#search');
  await search.pressSequentially(name.slice(0, 14), { delay: 20 });

  const result = page.locator('#search-results .result', { hasText: name });
  await expect(result).toBeVisible({ timeout: 5000 });

  // ut-docs#2174: the Designer's tiles are its live sale-screen replica's
  // inert [data-testid="designer-tile"] buttons (the flat #buttons-grid-admin
  // grid is retired); wait for the replica's first render before counting.
  await expect(page.locator('[data-testid="designer-categories"]')).toBeVisible();
  const tilesBefore = page.locator('[data-testid="designer-tile"] .tile-name', { hasText: name });
  // ut-docs#2541: every active item is a quick button by default, so the
  // imported codeless item is ALREADY a tile before anyone adds it -- the
  // card's "a new or imported item appears with no manual step" acceptance.
  await expect(tilesBefore).toHaveCount(1, { timeout: 5000 });
  await result.click();

  // Adding it explicitly (the ut-docs#1459 flow, pre-fix a 400) must
  // succeed and must not duplicate the tile: the explicit shortcut row
  // replaces the implicit one (dedupe by item id in ButtonStore.Load).
  await expect(page.locator('#buttons-add-error .error')).toHaveCount(0);
  await expect(tilesBefore).toHaveCount(1, { timeout: 5000 });

  const adminTile = page.locator('[data-testid="designer-tile"]', { hasText: name });
  const code = await adminTile.getAttribute('data-code');
  expect(code, 'added button must carry a real, non-empty code').toBeTruthy();
  expect(code).not.toBe('');

  // Step 3: it must actually ring up on the real sale screen, at the
  // right price -- the acceptance criterion a Go-level test alone can't
  // see (a synthesized code that only LOOKS valid but never resolves
  // through PriceResolverAdapter's live HTTP path would still pass every
  // unit test).
  await page.goto('/');
  await page.getByRole('tab', { name: category }).click();
  // ut-docs#2294: this same tile also exists in the All tab's own dedicated
  // grid (hidden here, since this category's own tab -- not All -- is
  // selected) -- scope to `.products-tab-panel` so the locator resolves to
  // the one real, visible instance rather than throwing a strict-mode
  // violation over the two matches.
  const tile = page.locator(`.products-tab-panel .btn-tile[data-name="${name}"]`);
  await expect(tile).toBeVisible();
  await tile.click();

  await expect(page.locator('#basket')).toContainText(name);
  await expect(page.locator('.basket .total')).toContainText('2.50');

  // Cleanup: remove the shortcut, deactivate the item, clear the basket --
  // this spec must not leave state for whichever spec runs next on the
  // shared dev server.
  // ut-docs#1465 (G41): a priced line's ✕ now only opens the void/comp/waste
  // reason sheet; the reason button inside it is what actually removes the
  // line. First reason button = Void, picked by position to stay
  // locale-independent.
  const basketRow = page.locator('#basket-lines tr', { hasText: name });
  await basketRow.locator('.shrinkage-remove-toggle').click();
  await basketRow.locator('.shrinkage-sheet-actions .btn').first().click();
  await expect(page.locator('#basket')).not.toContainText(name);
  await page.request.post('/api/buttons/remove', { form: { code: code! } });
  await page.goto('/catalog');
  const cleanupRow = page.locator('.catalog-row', { hasText: name });
  const id = await cleanupRow.first().getAttribute('data-id');
  if (id) {
    await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }

  assertClean();
});
