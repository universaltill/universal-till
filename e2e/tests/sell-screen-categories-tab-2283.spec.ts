import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#2283: an optional leading "Categories" tab on the sell screen
// (default OFF, toggled in Settings -> Display), showing one big tile per
// top-level category. Tapping a tile opens #category-picker-modal with
// that category's own product-tile grid (copied from the already-rendered
// per-category panel, then htmx.process()ed -- see app.js's utCategoryPicker
// and buttons.html's own #2283 comments for why).

const RUN = Date.now().toString(36).toUpperCase();
const CAT_PLAIN = `Sheet2283 Plain ${RUN}`;
const CAT_MOD = `Sheet2283 Mod ${RUN}`;
const CAT_EMPTY = `Sheet2283 Empty ${RUN}`;
const ITEM_PLAIN = { name: `Sheet2283 Item Plain ${RUN}`, sku: `S2283P${RUN}`, barcode: `S2283BC-P-${RUN}`, category: CAT_PLAIN };
const ITEM_MOD = { name: `Sheet2283 Item Mod ${RUN}`, sku: `S2283M${RUN}`, barcode: `S2283BC-M-${RUN}`, category: CAT_MOD };
const ITEM_EMPTY = { name: `Sheet2283 Item Empty ${RUN}`, sku: `S2283E${RUN}`, barcode: `S2283BC-E-${RUN}`, category: CAT_EMPTY };
type Item = typeof ITEM_PLAIN;

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Mirrors sell-tile-long-press-2285.spec.ts's own seedItems: a plain
// catalog import only creates `items` rows, so each item that should show
// as a sell-screen tile is ALSO added as a shortcut button via
// /api/buttons/add. ITEM_EMPTY is deliberately left WITHOUT a shortcut
// button, so CAT_EMPTY has zero active buttons and must not get a tile.
async function seedItems(page: Page, items: Item[], withButtons: Item[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: 'import-2283.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(csvFor(items)),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  for (const it of withButtons) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    const id = (await row.first().getAttribute('data-id'))!;
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id, label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
}

// Attaches a fresh modifier group ("Size" / "Regular") to ITEM_MOD via the
// real catalog UI -- same flow osk-decimal-sale-catalog-fields-1284.spec.ts's
// own createProbeItemAndOpenVariants/manage-modifiers-btn steps use.
async function attachModifierGroup(page: Page, itemName: string) {
  await page.goto('/catalog');
  const row = page.locator('.catalog-row', { hasText: itemName });
  await row.click();
  await page.locator('#item-form-tab-variants').click();
  await expect(page.locator('#catalog-variants')).toBeVisible();

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-groups-panel')),
    page.locator('#manage-modifiers-btn').click(),
  ]);
  await expect(page.locator('#modifier-groups-modal')).toBeVisible();

  await page.locator('.modifier-admin-group-new input[name="name"]').fill('Size');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-group')),
    page.locator('.modifier-admin-group-new button[type=submit]').click(),
  ]);
  const group = page.locator('.modifier-admin-group').filter({ has: page.locator('input[name="name"][value="Size"]') });
  await expect(group).toBeVisible();

  await group.locator('form.modifier-admin-option-row:not(:has(input[name="id"])) input[name="name"]').fill('Regular');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-option')),
    group.locator('form.modifier-admin-option-row:not(:has(input[name="id"])) button[type=submit]').click(),
  ]);
  await expect(group.locator('form.modifier-admin-option-row:has(input[name="id"])')).toBeVisible();

  await page.locator('#modifier-groups-modal').evaluate((el: HTMLDialogElement) => el.close());
  await closeItemForm(page);
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

async function setCategoriesTab(page: Page, enabled: boolean) {
  const resp = await page.request.post('/api/settings/categories-tab', { form: { enabled: String(enabled) } });
  expect(resp.ok(), `set categories-tab enabled=${enabled}`).toBe(true);
}

test.describe('Sell-screen Categories tab (ut-docs#2283)', () => {
  test('gated by the setting, tiles open the right category, nested modifier picker still works, touch target size', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setCategoriesTab(page, false);
    await seedItems(page, [ITEM_PLAIN, ITEM_MOD, ITEM_EMPTY], [ITEM_PLAIN, ITEM_MOD]);
    await attachModifierGroup(page, ITEM_MOD.name);

    try {
      // (1) Off by default (and explicitly turned off above): no Categories
      // tab at all.
      await page.goto('/');
      await expect(page.locator('#cat-tab-categories')).toHaveCount(0);

      // (2) Turn it on -- the tab appears, first, before All.
      await setCategoriesTab(page, true);
      await page.reload();
      const categoriesTab = page.locator('#cat-tab-categories');
      await expect(categoriesTab).toBeVisible();
      const allTab = page.locator('#cat-tab-all');
      const catBox = (await categoriesTab.boundingBox())!;
      const allBox = (await allTab.boundingBox())!;
      expect(catBox.x, 'Categories tab must render before (start of) the All tab').toBeLessThanOrEqual(allBox.x);

      // (3) A category with zero active shortcut buttons (CAT_EMPTY) gets
      // no tile at all, even though the category itself exists.
      await categoriesTab.click();
      const tilePlain = page.locator(`.category-tile[data-category-name="${CAT_PLAIN}"]`);
      const tileMod = page.locator(`.category-tile[data-category-name="${CAT_MOD}"]`);
      const tileEmpty = page.locator(`.category-tile[data-category-name="${CAT_EMPTY}"]`);
      await expect(tilePlain).toBeVisible();
      await expect(tileMod).toBeVisible();
      await expect(tileEmpty).toHaveCount(0);

      // (4) Touch target size: at least as large as an existing product
      // tile, and at least the product's own documented 46px floor, at the
      // 1024x600 kiosk floor this spec runs at (playwright.config.ts).
      const tilePlainBox = (await tilePlain.boundingBox())!;
      expect(tilePlainBox.height).toBeGreaterThanOrEqual(46);
      expect(tilePlainBox.width).toBeGreaterThanOrEqual(46);
      await allTab.click();
      const productTile = page.locator(`.btn-tile[data-name="${ITEM_PLAIN.name}"]`);
      await expect(productTile).toBeVisible();
      const productBox = (await productTile.boundingBox())!;
      expect(tilePlainBox.height, 'category tile must be at least as tall as a product tile').toBeGreaterThanOrEqual(productBox.height);
      await categoriesTab.click();

      // (5) Tapping a category tile with active items opens the modal with
      // that category's items.
      const modal = page.locator('#category-picker-modal');
      await tilePlain.click();
      await expect(modal).toBeVisible();
      const plainInModal = modal.locator(`.btn-tile[data-name="${ITEM_PLAIN.name}"]`);
      await expect(plainInModal).toBeVisible();
      await expect(modal.locator(`.btn-tile[data-name="${ITEM_MOD.name}"]`)).toHaveCount(0);

      // (6) A plain item's tile inside the modal still scans straight to
      // the basket.
      await plainInModal.click();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await expect(page.locator('.basket .line-name').first()).toHaveText(ITEM_PLAIN.name);
      await page.locator('#category-picker-modal .modifier-actions .btn.secondary').click();
      await expect(modal).toBeHidden();

      // (7) A modifier-item's tile inside the modal opens ITS OWN modifier
      // picker (#modifier-modal), not the plain scan path -- the
      // nested-htmx.process() case the card explicitly calls out.
      await tileMod.click();
      await expect(modal).toBeVisible();
      const modInModal = modal.locator(`.btn-tile[data-name="${ITEM_MOD.name}"]`);
      await expect(modInModal).toBeVisible();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
        modInModal.click(),
      ]);
      await expect(page.locator('#modifier-modal')).toBeVisible();
      await expect(page.locator('#modifier-modal')).toContainText('Size');
      await expect(page.locator('#modifier-modal')).toContainText('Regular');
      await page.keyboard.press('Escape');
      await expect(page.locator('#modifier-modal')).toBeHidden();

      assertClean();
    } finally {
      await setCategoriesTab(page, false);
      await cleanupItems(page, [ITEM_PLAIN, ITEM_MOD, ITEM_EMPTY]);
    }
  });
});
