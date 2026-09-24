import { test, expect } from './fixtures';
import { watchConsole, closeItemForm } from './helpers';

// ut-docs#2314: editing an item's price from the catalog form used to write
// ONLY items.base_price — but the till actually charges whatever
// price_history resolves (an active open-ended row wins over base_price,
// internal/data/pos_repo.go's ResolveCurrentPrice / catalog_repo.go's
// ItemCurrentPrices). Any item that has ever carried an active
// price_history row (the demo seed's Coca-Cola/Pepsi/Sparkling Water all
// do — internal/data/seeddata/demo_catalogue.sql) went on charging its OLD
// price forever after a "Saved" edit, with no error anywhere.
//
// This uses the demo-seeded "Coca-Cola Can 330ml" (itm001) specifically
// BECAUSE it ships with an active price_history row (ph001, price 120,
// starts 2025-01-01, no ends_at) whose price happens to equal base_price
// (120) — so editing it is the exact repro: pre-fix, the edit only moved
// base_price, and price_history's still-open 120 row kept winning at
// resolve time forever.
//
// The fix's OWN scope (see ut-docs#2314's brief) makes the catalog row's
// data-price attribute (catalog_row.html) carry the RESOLVED price, not
// raw base_price, and the edit form's Price field reads data-price
// straight off the DOM (catalog.html's openCatalogRow) — so the row-level
// OOB swap after save (ut-docs#1363) already proves the price_history
// round-trip without leaving /catalog at all. The sale-screen tile and the
// basket are then checked after a real navigation back to `/` (the same
// full-page `location.assign` the sell-screen tile-sheet's own "Edit in
// catalog" deep link uses, web/ui/pages/catalog.html) — not an in-place
// htmx `buttons-changed` swap, since /catalog and `/` are two separate top-
// level pages here and a full nav already re-renders the sale-screen grid
// from ItemCurrentPrices (ut-docs#2258) on its own. `buttons-changed` is
// the mechanism an operator staying ON `/` sees after a Move/Remove from
// the tile-sheet (ut-docs#2285); a catalog price edit doesn't have an
// in-page equivalent to trigger, so this spec exercises the real path a
// price edit actually takes back to the sale screen instead.
test.describe('catalog price edit updates price_history (ut-docs#2314)', () => {
  const ITEM_NAME = 'Coca-Cola Can 330ml';
  // The demo seed's shortcut_buttons.label for itm001 — separate from the
  // item's own catalog name above. The sale-screen tile and the basket
  // line both render this label (buttons.html's data-name/.tile-name,
  // basket.html's .line-name all come from the shortcut/basket-line's own
  // Label, not the item's Name), confirmed by driving this spec for real:
  // the basket line reads "Coca-Cola 330ml", never "Coca-Cola Can 330ml".
  const TILE_LABEL = 'Coca-Cola 330ml';
  const ORIGINAL_PRICE_MAJOR = '1.20';
  const ORIGINAL_PRICE_MINOR = '120';
  const NEW_PRICE_MAJOR = '4.75';
  const NEW_PRICE_MINOR = '475';

  // Strips everything but digits, so "£4.75" / "4,75 €" / "CHF 4.75" all
  // reduce to "475" — this spec only cares that the MINOR-unit amount
  // shown matches, not this till's configured currency symbol/decimals.
  const digitsOf = (text: string) => (text || '').replace(/\D/g, '');

  test('saving a new price ends the active price_history row and the till charges the new price', async ({ page }) => {
    const assertClean = watchConsole(page);

    await page.goto('/catalog');
    const row = page.locator(`.catalog-row[data-name="${ITEM_NAME}"]`);
    await expect(row).toBeVisible();
    const itemId = await row.getAttribute('data-id') as string;

    // Precondition: the demo seed's active price_history row (120) equals
    // base_price (120) today, so this also incidentally proves the fix
    // didn't just start showing base_price outright — see the second
    // assertion below (after editing to a DIFFERENT price) for the part
    // that actually distinguishes "resolved price" from "raw base_price".
    await expect(row).toHaveAttribute('data-price', ORIGINAL_PRICE_MINOR);

    try {
      // Open the edit form the same way an operator does — click the card
      // (catalog-active-checkbox-1367.spec.ts's own established pattern for
      // editing an EXISTING item, not openNewItemForm).
      await row.click();
      await expect(page.locator('#item-id')).toHaveValue(itemId);
      // The Price field must be pre-filled with the RESOLVED price (120),
      // which today happens to equal base_price — this only becomes a
      // meaningful assertion once combined with the post-edit check below.
      await expect(page.locator('#item-price')).toHaveValue(ORIGINAL_PRICE_MAJOR);

      await page.locator('#item-price').fill(NEW_PRICE_MAJOR);
      await page.locator('#item-form-submit').click();
      await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

      // The row-level OOB swap (ut-docs#1363) lands in the SAME response —
      // no navigation, no extra wait needed. Its data-price must now be the
      // NEW resolved price: the old price_history row (120) closed and a
      // new one (475) opened, so ItemCurrentPrices/ResolveCurrentPrice both
      // return 475 from this moment on, not the update's raw base_price
      // colliding with a still-open 120 row (the pre-fix bug).
      await expect(row).toHaveAttribute('data-price', NEW_PRICE_MINOR);

      await closeItemForm(page);

      // Back to the sale screen the same way the sell-screen tile-sheet's
      // "Edit in catalog" link would send an operator home (location.assign
      // to `return`) — a real navigation, so this is the sale-screen grid's
      // OWN fresh render, not a live in-page patch.
      await page.goto('/');
      // The sale-screen tile's data-name is the shortcut button's OWN label
      // ("Coca-Cola 330ml", shorter than the item's real name), a separate
      // string from the catalog row's data-name (item.Name, "Coca-Cola Can
      // 330ml") — buttons.html sets data-name="{{ .Label }}". data-item-id
      // ties directly to the item this test just edited, sidestepping that
      // label/name mismatch entirely (same attribute
      // sell-tile-long-press-2285.spec.ts already keys off).
      // Drinks isn't necessarily the default tab (the strip's first
      // category is), so select it first and scope to
      // `.products-tab-panel`: this test is specifically about the
      // quick-button/basket-line label path (see TILE_LABEL's own comment
      // above), whose tile carries the shortcut's own barcode/label
      // ("Coca-Cola 330ml"), not the catalog item's raw Name.
      await page.getByRole('tab', { name: 'Drinks' }).click();
      const tile = page.locator(`.products-tab-panel .btn-tile[data-item-id="${itemId}"]`);
      await expect(tile).toBeVisible();
      await expect(async () => {
        expect(digitsOf(await tile.locator('.tile-price').innerText())).toBe(NEW_PRICE_MINOR);
      }).toPass();

      // Add it to the basket — the real checkout path, not just a display
      // check: this is what the original bug report said stayed broken
      // ("the till goes on charging the old price forever").
      await tile.click();
      const line = page.locator('#basket-lines tr').filter({ has: page.locator('.line-name', { hasText: TILE_LABEL }) });
      await expect(line).toBeVisible();
      const unitPriceCell = line.locator('td').nth(2); // name/qty-inputs, then unit price, then line total
      expect(digitsOf(await unitPriceCell.innerText())).toBe(NEW_PRICE_MINOR);

      assertClean();
    } finally {
      // Restore the demo item's price so no later spec inherits a changed
      // Coca-Cola price (same "leave shared state as found" convention as
      // sell-tile-long-press-2285.spec.ts's own move-back/re-add steps).
      await page.request.post('/api/pos/reset');
      await page.goto('/catalog');
      const restoreRow = page.locator(`.catalog-row[data-name="${ITEM_NAME}"]`);
      await restoreRow.click();
      await page.locator('#item-price').fill(ORIGINAL_PRICE_MAJOR);
      await page.locator('#item-form-submit').click();
      await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
      await expect(restoreRow).toHaveAttribute('data-price', ORIGINAL_PRICE_MINOR);
      await closeItemForm(page);
    }
  });
});
