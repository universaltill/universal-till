import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1356: bulk "backfill barcodes from SKU" on the Catalog page —
// preview-before-apply, reusing #1224's exact SKU→barcode derivation. This
// drives the real flow end to end: create a barcode-less item, open the
// backfill dialog, see it in the preview, confirm, and see its derived
// barcode as a chip on the (reloaded) catalog page.
test('backfilling barcodes from SKU previews then assigns a derived barcode to a barcode-less item', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/catalog');

  // A barcode-less item with a real SKU: leave the Barcode field on the
  // form untouched (only SKU + name + price), the exact shape
  // ItemsWithoutBarcode targets.
  const stamp = Date.now();
  const name = 'Backfill Probe ' + stamp;
  const sku = 'E2EBF' + stamp;
  await page.locator('#item-name').fill(name);
  await page.locator('#item-sku').fill(sku);
  await page.locator('#item-price').fill('3.00');
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

  const row = page.locator('.catalog-row', { hasText: name });
  await expect(row).toBeVisible();
  // No barcode yet: the row's own "Barcodes: …" summary line is absent.
  await expect(row).not.toContainText('Barcodes:');

  // Open the backfill dialog and see the preview.
  await page.locator('#catalog-barcode-backfill-btn').click();
  const dialog = page.locator('#barcode-backfill-modal');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(name);
  await expect(dialog).toContainText(sku);

  // Regression guard for a real bug found by screenshot during testing (not
  // caught by any prior assertion): app.css's shared `table.table` rule sets
  // `overflow: hidden` on the table element itself, and with the dialog's
  // base .modifier-modal width (28rem) an unbreakable barcode string doesn't
  // fit — the text was silently clipped mid-digit with no ellipsis, no
  // scrollbar, nothing telling the operator it was cut off. A bounding-box
  // comparison against the dialog does NOT catch this: `overflow: hidden`
  // clips PAINTED content, not the element's own layout box, so the cell's
  // box always measured as fitting even while its text visibly didn't. The
  // real signal is the cell's own scrollWidth exceeding its clientWidth —
  // i.e. its content doesn't fit within itself, so *some* of it is unread.
  const barcodeCell = dialog.locator('td', { hasText: sku }).last();
  await expect(barcodeCell).toContainText(sku);
  const clipped = await barcodeCell.evaluate(el => el.scrollWidth > el.clientWidth + 1);
  expect(clipped, 'derived-barcode cell must not clip its own text').toBe(false);

  // Confirm — the commit response swaps the result fragment into the SAME
  // dialog (deliberately NOT HX-Refresh: htmx would process that header
  // before the swap and reload the page before the operator ever saw the
  // report, review finding ut-docs#1356). So the report must be readable
  // here, before anything reloads.
  await dialog.getByRole('button', { name: /Assign these barcodes/i }).click();
  await expect(dialog).toContainText('Assigned 1 barcode');

  // Only the operator's own "Close" click reloads the page (same
  // close-then-reload shape as plugin_install_modal.html) — wait for that
  // navigation before asserting anything about post-commit DOM state.
  await Promise.all([
    page.waitForNavigation(),
    dialog.getByRole('button', { name: 'Close' }).click(),
  ]);

  // After the reload, the item's row shows its newly derived barcode — the
  // SKU itself, since a plain alphanumeric SKU derives verbatim under the
  // shop's default enabled symbologies (CODE128 catch-all, ut-docs#1224).
  const refreshedRow = page.locator('.catalog-row', { hasText: name });
  await expect(refreshedRow).toBeVisible();
  await expect(refreshedRow).toContainText('Barcodes:');
  await expect(refreshedRow).toContainText(sku);

  assertClean();
});
