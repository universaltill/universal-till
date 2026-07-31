import path from 'path';
import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The flow Farshid asked for directly: upload a catalog item's photo, then
// confirm it actually shows up on the till when that item is scanned —
// this is exactly the class of bug this session already found and fixed
// twice in the Go layer (images written to the wrong path; a stale
// cache-busting version serving old bytes) but could never PROVE fixed
// without a real browser actually loading the <img>. Uses Sparkling Water
// 500ml (itm003, barcode 5000000000036) — untouched by any other spec.
test('uploading a catalog item photo makes it appear on the till', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.goto('/catalog');
  await expect(page.locator('body')).toContainText('Catalog');

  // Select the item (fills the image-upload panel's hidden fields).
  await page.locator('.catalog-row', { hasText: 'Sparkling Water 500ml' }).click();

  // Open the collapsed "Item image" panel and upload a real file.
  await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();
  await page.locator('#image-file').setInputFiles(path.join(__dirname, '../fixtures/test-item-image.png'));
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');

  // The catalog table's own thumbnail reflects the upload without a page
  // reload (the upload handler swaps #catalog-table in place). itm003
  // ships with a SEEDED 289x375 thumbnail, so asserting merely "non-zero
  // width" would pass even with a completely broken/no-op upload — the
  // fixture is a distinctive 2x2, so assert that exact dimension to
  // actually prove the new bytes made it through, not just that some
  // (possibly stale, possibly the old seed) image happens to load.
  const catalogThumb = page.locator('.catalog-row', { hasText: 'Sparkling Water 500ml' }).locator('img.thumb');
  await expect(catalogThumb).toHaveJSProperty('complete', true);
  await expect(catalogThumb).toHaveJSProperty('naturalWidth', 2);

  // Now scan it at the till and confirm the SAME uploaded image (not the
  // old seeded one) renders on the basket line — not just that the
  // upload "succeeded" server-side.
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000036');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Sparkling Water');

  const basketThumb = page.locator('#basket .line-thumb');
  await expect(basketThumb).toBeVisible();
  await expect(basketThumb).toHaveJSProperty('complete', true);
  await expect(basketThumb).toHaveJSProperty('naturalWidth', 2);

  // Clear the basket line so this spec doesn't leave a dangling item for
  // whichever spec runs next (shared server-side basket).
  await page.locator('#basket-lines tr', { hasText: 'Sparkling Water' })
    .locator('.btn-x').click();
  await expect(page.locator('#basket')).not.toContainText('Sparkling Water');

  assertClean();
});
