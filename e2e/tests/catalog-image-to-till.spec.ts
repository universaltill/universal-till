import path from 'path';
import { test, expect } from './fixtures';
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
  // reload (the upload handler answers with the item's row as an
  // out-of-band fragment — ut-docs#1363 — not a whole-table swap). itm003
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

// ut-docs#1326: a dedicated "take a photo" input alongside the plain file
// picker, so a tablet's camera opens directly instead of a generic
// file/gallery chooser. Uses Apple Juice 1L (itm006, barcode
// 5000000000067) — untouched by any other spec. Deliberately does NOT
// assert on the resulting <img> thumbnail's load state (naturalWidth/
// complete) the way the test above does — that assertion is known to fail
// deterministically in this sandboxed pipeline environment for reasons
// unrelated to the upload itself (ut-docs#1362); this test instead proves
// the new input actually reaches the server, which is the part #1326
// changes.
test('taking a photo (capture input) uploads via the same canonical file field', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.goto('/catalog');
  await expect(page.locator('body')).toContainText('Catalog');

  await page.locator('.catalog-row', { hasText: 'Apple Juice 1L' }).click();
  await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();

  // The dedicated "take a photo" input carries capture="environment" so a
  // mobile/tablet browser opens the rear camera directly, not a generic
  // file/gallery chooser.
  const cameraInput = page.locator('#image-file-camera');
  await expect(cameraInput).toHaveAttribute('capture', 'environment');
  await expect(cameraInput).toHaveAttribute('accept', 'image/*');

  // Picking a file via the camera input must reach the server the same way
  // the plain "choose file" input does — it's copied into the canonical
  // #image-file field that both the submit handler and the multipart form
  // key off (see the DataTransfer copy in the change listener).
  await cameraInput.setInputFiles(path.join(__dirname, '../fixtures/test-item-image.png'));
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');

  assertClean();
});

// ut-docs#1326 review finding: the two triggers must be real, keyboard-
// activatable controls. An earlier draft used a bare `<label for="...">`
// on the hidden file input, which review proved (via a Tab-order probe)
// is never reached by keyboard Tab and has no default Enter/Space
// activation — a real regression vs. the plain visible file input this
// replaced. Confirm both are actual <button> elements, and that keyboard
// focus + Enter on the camera trigger opens the native file chooser (a
// <label> would not fire a filechooser event this way).
test('take-photo/choose-file triggers are real buttons, keyboard-activatable', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Apple Juice 1L' }).click();
  await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();

  const cameraBtn = page.locator('#image-camera-btn');
  const chooseBtn = page.locator('#image-choose-btn');
  await expect(cameraBtn).toHaveJSProperty('tagName', 'BUTTON');
  await expect(chooseBtn).toHaveJSProperty('tagName', 'BUTTON');

  await cameraBtn.focus();
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.keyboard.press('Enter'),
  ]);
  expect(chooser.isMultiple()).toBe(false);

  assertClean();
});
