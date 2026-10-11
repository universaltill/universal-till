import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';
import path from 'path';

// ut-docs#3127: a shop WITHOUT background removal (the shared default till
// configures none) sees the item, category and variant photo pickers
// exactly as before — no "Remove background" panel, a variant photo still
// uploads the moment it is picked, and the cutout endpoint answers 404.
// The configured case is photo-cutout-3127.spec.ts (project `cutout`).
const FIXTURE = path.join(__dirname, '../fixtures/test-item-image.png');
const RUN = Date.now().toString(36).toUpperCase();

test('AI off: no offer on the item, category or variant picker; the old flows work', async ({ page }) => {
  const assertClean = watchConsole(page);

  // Item form: pick → no panel; Upload Image saves as before.
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Whole Milk 1L' }).click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator('.photo-cutout')).toHaveCount(0);
  await page.locator('#image-file').setInputFiles(FIXTURE);
  await expect(page.locator('.photo-cutout')).toHaveCount(0);
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');
  await closeItemForm(page);

  // Variant: the pick uploads straight away, no offer in between.
  const name = `Cutout Off ${RUN}`;
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('3.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await closeItemForm(page);
  const itemId = (await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id'))!;
  await page.locator('.catalog-row', { hasText: name }).click();
  await page.locator('#item-form-tab-variants').click();
  await page.locator('input[form="vf-new"][name="name"]').fill('Small');
  await page.locator('input[form="vf-new"].variant-price-major').fill('1.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  await expect(page.locator('.photo-cutout')).toHaveCount(0);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant/image') && r.request().method() === 'POST'),
    page.locator('.vg-row:not(.vg-new) .vg-img-upload input[type=file]').setInputFiles(FIXTURE),
  ]);
  await expect(page.locator('.vg-row:not(.vg-new) .vg-img img')).toHaveAttribute('src', new RegExp(`/variants/[^/]+/thumb\\.png`));
  await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } });

  // Category form: pick → no panel.
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await page.locator('#category-image-file').setInputFiles(FIXTURE);
  await expect(page.locator('#category-image-file-name')).toHaveText('test-item-image.png');
  await expect(page.locator('.photo-cutout')).toHaveCount(0);

  // And the endpoint itself is off.
  const res = await page.request.post('/api/catalog/image/cutout', {
    multipart: { photo: { name: 'p.png', mimeType: 'image/png', buffer: Buffer.from('x') } },
  });
  expect(res.status()).toBe(404);
  assertClean();
});
