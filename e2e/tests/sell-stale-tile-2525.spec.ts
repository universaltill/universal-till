import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2525: a sale-screen tile is a snapshot of the catalog from when the
// grid last rendered. If the item is switched off somewhere else while the
// sale screen stays open (another till, my., a main-till sync; here: a
// direct API call standing in for "another device"), tapping the stale tile
// must say the buttons were out of date and refresh the grid by itself. It
// must not end in a bare "Item not found" that repeats on every retry, and a
// modifier/variant tile must not open the picker (which would still hold the
// previous item's options).
//
// This drives what the Go handler tests can't see: htmx acting on the
// HX-Trigger/HX-Retarget headers, the grid's own buttons-changed re-fetch,
// and the tile's after-request guard around showModal().

const RUN = Date.now().toString(36).toUpperCase();
type Seeded = { itemId: string; name: string; barcode: string; category: string };

async function seedTileItem(page: Page, tag: string, withModifier: boolean): Promise<Seeded> {
  const name = `Stale2525 ${tag} ${RUN}`;
  const barcode = `STALE2525-${tag}-${RUN}`;
  const category = `Stale2525 Cat ${tag} ${RUN}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},S2525${tag}${RUN},${barcode},1.00,${category},1\n`;

  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-stale2525-${tag}-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(csv),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  await expect(row).toHaveCount(1);
  const itemId = (await row.first().getAttribute('data-id'))!;
  const addResp = await page.request.post('/api/buttons/add', { form: { itemId, label: name, code: barcode } });
  expect(addResp.ok(), 'add quick button').toBe(true);

  if (withModifier) {
    const groupName = `Size ${tag} ${RUN}`;
    const groupResp = await page.request.post('/api/catalog/modifier-group', {
      form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
    });
    expect(groupResp.ok(), 'create modifier group').toBe(true);
  }
  return { itemId, name, barcode, category };
}

async function openTile(page: Page, seeded: Seeded) {
  await page.goto('/');
  // The category may sit in the overflow menu on a crowded shared DB, so
  // select its tab directly (Alpine's own @click) rather than by position.
  await page.locator('[data-cat-tab]', { hasText: seeded.category }).evaluate((el) => (el as HTMLElement).click());
  const tile = page.locator('.btn-tile:visible', { hasText: seeded.name });
  await expect(tile).toBeVisible();
  return tile;
}

test.describe('ut-docs#2525 stale sale-screen tile', () => {
  let seeded: Seeded | null = null;

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset').catch(() => {});
    if (seeded) await page.request.post('/api/buttons/remove', { form: { code: seeded.barcode } }).catch(() => {});
    seeded = null;
  });

  test('plain tile: deactivated elsewhere -> "out of date" toast and the grid drops the tile', async ({ page }) => {
    seeded = await seedTileItem(page, 'P', false);
    const tile = await openTile(page, seeded);
    const assertClean = watchConsole(page);

    // "Another device" switches the item off while this sale screen is open.
    const off = await page.request.post('/api/catalog/item/deactivate', { form: { id: seeded.itemId } });
    expect(off.ok(), 'deactivate').toBe(true);

    const refetch = page.waitForResponse((r) => r.url().includes('/ui/buttons') && r.request().method() === 'GET');
    await tile.click();
    await expect(page.locator('#basket')).toContainText('out of date');
    await expect(page.locator('#basket')).not.toContainText('Item not found');
    await refetch;
    // The re-fetched grid no longer carries the tile at all.
    await expect(page.locator('.btn-tile', { hasText: seeded.name })).toHaveCount(0);
    await expect(page.locator('#basket')).not.toContainText(seeded.name);
    assertClean();
  });

  test('modifier tile: deactivated elsewhere -> toast, grid refresh, picker stays shut', async ({ page }) => {
    seeded = await seedTileItem(page, 'M', true);
    const tile = await openTile(page, seeded);
    const assertClean = watchConsole(page);

    const off = await page.request.post('/api/catalog/item/deactivate', { form: { id: seeded.itemId } });
    expect(off.ok(), 'deactivate').toBe(true);

    const refetch = page.waitForResponse((r) => r.url().includes('/ui/buttons') && r.request().method() === 'GET');
    await tile.click();
    await expect(page.locator('#basket')).toContainText('out of date');
    await refetch;
    await expect(page.locator('#modifier-modal')).not.toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: seeded.name })).toHaveCount(0);
    assertClean();
  });
});
