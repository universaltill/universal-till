import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2614: the Designer's "Hidden from sell screen (N)" section has a
// one-click "Show all N on the sell screen" (POST /api/buttons/unhide-all)
// that puts every hidden item back as a sell-screen tile. Same setup as
// sell-tile-hide-delete-2541.spec.ts (CSV import, then hide via the API the
// tile badge itself posts to).

const RUN = Date.now().toString(36).toUpperCase();
const CAT = `UnhideAll2614 Cat ${RUN}`;
const A = { name: `UnhideAll2614 Item A ${RUN}`, sku: `UHA2614A${RUN}`, barcode: `UHA2614BC-A-${RUN}` };
const B = { name: `UnhideAll2614 Item B ${RUN}`, sku: `UHA2614B${RUN}`, barcode: `UHA2614BC-B-${RUN}` };

async function importItems(page: Page) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-2614-${RUN}.csv`,
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

test.describe('Designer: show all hidden items on the sell screen (ut-docs#2614)', () => {
  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    for (const it of [A, B]) {
      const id = await itemId(page, it.name);
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
    }
    await page.close();
  });

  test('hide two items, then Show all brings both tiles back', async ({ page }) => {
    const assertClean = watchConsole(page);
    await importItems(page);
    const ids: string[] = [];
    for (const it of [A, B]) ids.push((await itemId(page, it.name))!);

    for (const id of ids) {
      const res = await page.request.post('/api/buttons/hide', { form: { itemId: id } });
      expect(res.ok(), `hide ${id}`).toBe(true);
    }
    // With every item in it hidden, the category drops out of the strip
    // entirely (hidden items don't count toward showing it).
    await page.goto('/');
    await expect(page.locator('.products-tab-panel')).not.toHaveCount(0);
    await expect(page.getByRole('tab', { name: CAT })).toHaveCount(0);
    await expect(tile(page, A.name)).toHaveCount(0);
    await expect(tile(page, B.name)).toHaveCount(0);

    await page.goto('/designer');
    for (const id of ids) await expect(page.getByTestId(`designer-hidden-item-${id}`)).toBeVisible();
    const showAll = page.getByTestId('designer-hidden-unhide-all');
    await expect(showAll).toBeVisible();
    await expect(showAll).toContainText(/Show all \d+ on the sell screen/);
    const box = await showAll.boundingBox();
    expect(box!.height, 'touch target >= 44px').toBeGreaterThanOrEqual(44);
    await page.getByTestId('designer-hidden').screenshot({ path: test.info().outputPath('designer-hidden-show-all.png') });

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/unhide-all') && r.ok()),
      showAll.click(),
    ]);
    for (const id of ids) await expect(page.getByTestId(`designer-hidden-item-${id}`)).toHaveCount(0);
    await expect(page.getByTestId('designer-hidden-empty')).toBeVisible();
    await expect(page.getByTestId('designer-hidden-unhide-all')).toHaveCount(0);

    await page.goto('/');
    await page.getByRole('tab', { name: CAT }).click();
    await expect(tile(page, A.name)).toBeVisible();
    await expect(tile(page, B.name)).toBeVisible();

    assertClean();
  });
});
