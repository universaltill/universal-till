import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';
import zlib from 'zlib';

// ut-docs#2500 (slice 1): a category gets an image — a built-in icon or an
// uploaded photo — set in the /categories dialog, and it shows on the sell
// screen's category tiles, strip tabs and "…" overflow tiles. The same
// card compacts the category tile: the product owner found ~170px tiles
// on the real 1280x800 tablet fit only three rows. Pinned here in a real
// browser:
//   (a) the dialog's image picker round-trips (icon pressed on reopen, an
//       upload shows its preview with no icon pressed, "No image" clears);
//   (b) the images render (and actually load) on the category tile, the
//       strip tab and the overflow tile;
//   (c) geometry at 1024x600 and 360x800: a category tile is no taller
//       than an item tile and never under the 44px touch floor.
//
// HONESTY NOTE: Chromium with synthetic taps. How it looks on the pilot
// tablet's WebView is the local hardware lane's to confirm.

const RUN = Date.now().toString(36).toUpperCase();
const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const ICONS = '#category-icon-grid';

// A real 24x24 solid-colour PNG, built by hand so the spec needs no fixture
// file (zlib is Node's own).
function solidPNG(r: number, g: number, b: number, size = 24): Buffer {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf: Buffer) => {
    let c = 0xffffffff;
    for (const byte of buf) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
    const c = Buffer.alloc(4);
    c.writeUInt32BE(crc(td));
    return Buffer.concat([len, td, c]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // RGB
  const raw = Buffer.alloc((size * 3 + 1) * size);
  for (let y = 0; y < size; y++) {
    const o = y * (size * 3 + 1);
    for (let x = 0; x < size; x++) {
      raw[o + 1 + x * 3] = r;
      raw[o + 2 + x * 3] = g;
      raw[o + 3 + x * 3] = b;
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// Every successful dialog Save answers HX-Redirect — a real navigation back
// to /categories; await the NEW document's load (categories-record-dialog-
// 2010.spec.ts's clickThenReload, ut-docs#2345).
async function save(page: Page) {
  await Promise.all([page.waitForEvent('load'), page.locator(`${DIALOG} .record-dialog-save`).click()]);
}

function row(page: Page, name: string) {
  return page.locator('#categories-table .category-row', { hasText: name }).first();
}

type Seeded = { iconCat: string; photoCat: string; iconCatId: string; photoCatId: string; itemIds: string[] };

// Each test seeds its own uniquely-named categories: a category is only
// ever deactivated (never deleted), so a second seed reusing a name would
// find the first test's retired row too.
let seedCount = 0;
async function seed(page: Page): Promise<Seeded> {
  const tag = `${RUN}${++seedCount}`;
  const iconCat = `Img2500 Icon ${tag}`;
  const photoCat = `Img2500 Photo ${tag}`;
  await page.goto('/categories');

  // Create with a built-in icon.
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await expect(page.locator(`${ICONS} [data-icon="none"]`)).toHaveAttribute('aria-pressed', 'true');
  await page.locator(NAME).fill(iconCat);
  await page.locator(`${ICONS} [data-icon="coffee"]`).click();
  await expect(page.locator(`${ICONS} [data-icon="coffee"]`)).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator(`${ICONS} [data-icon="none"]`)).toHaveAttribute('aria-pressed', 'false');
  await save(page);

  // Create with an uploaded photo (the hidden input behind Choose File).
  await page.locator('#categories-new').click();
  await page.locator(NAME).fill(photoCat);
  await page.locator('#category-image-file').setInputFiles({ name: 'cat.png', mimeType: 'image/png', buffer: solidPNG(220, 60, 30) });
  await expect(page.locator('#category-image-file-name')).toHaveText('cat.png');
  await expect(page.locator(`${ICONS} [aria-pressed="true"]`)).toHaveCount(0);
  await save(page);

  const iconCatId = (await row(page, iconCat).getAttribute('data-id'))!;
  const photoCatId = (await row(page, photoCat).getAttribute('data-id'))!;
  // ut-docs#2717: a library pick stores its icon id (the id my. uses),
  // not a path; an upload stores the path and no icon.
  await expect(row(page, iconCat)).toHaveAttribute('data-image', '');
  await expect(row(page, iconCat)).toHaveAttribute('data-icon-id', 'lucide:coffee');
  await expect(row(page, photoCat)).toHaveAttribute('data-icon-id', '');
  await expect(row(page, photoCat)).toHaveAttribute('data-image', `/public/assets/categories/${photoCatId}/thumb.png`);

  // One item per category (tiles only show categories with active items),
  // with a two-word name so the item tile is a realistic one.
  const itemIds: string[] = [];
  for (const [catId, label] of [[iconCatId, 'Latte'], [photoCatId, 'Tomato Soup']]) {
    const itemName = `Img2500 ${label} ${tag}`;
    const resp = await page.request.post('/api/catalog/item', { form: { name: itemName, price: '250', categoryId: catId } });
    expect(resp.ok(), `create item ${itemName}`).toBe(true);
  }
  await page.goto('/catalog');
  for (const label of ['Latte', 'Tomato Soup']) {
    const r = page.locator(`.catalog-row[data-name="Img2500 ${label} ${tag}"]`);
    await expect(r).toHaveCount(1);
    itemIds.push((await r.first().getAttribute('data-id'))!);
  }
  return { iconCat, photoCat, iconCatId, photoCatId, itemIds };
}

async function cleanup(page: Page, s: Seeded | null) {
  if (!s) return;
  for (const id of s.itemIds) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  for (const id of [s.iconCatId, s.photoCatId]) {
    await page.request.post(`/api/categories/${id}/active`, { form: { active: '0' }, maxRedirects: 0 });
  }
}

async function imgLoaded(page: Page, selector: string): Promise<boolean> {
  return page.locator(selector).evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0);
}

test.describe('category image + compact tiles (ut-docs#2500)', () => {
  let seeded: Seeded | null = null;

  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
    await cleanup(page, seeded);
    seeded = null;
    await page.request.post('/api/pos/reset');
  });

  test('(a)+(b) picker round-trips; images render on tile, strip tab and overflow tile', async ({ page }) => {
    const assertClean = watchConsole(page);
    seeded = await seed(page);
    const s = seeded;
    await page.goto('/categories');

    // (a) Reopen: the icon category shows coffee pressed; the photo
    // category shows its preview and no icon pressed.
    await row(page, s.iconCat).click();
    await expect(page.locator(`${ICONS} [data-icon="coffee"]`)).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('#category-image-current')).toBeHidden();
    await page.locator(NAME).press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await row(page, s.photoCat).click();
    await expect(page.locator(`${ICONS} [aria-pressed="true"]`)).toHaveCount(0);
    await expect(page.locator('#category-image-current')).toBeVisible();
    expect(await imgLoaded(page, '#category-image-preview')).toBe(true);
    await page.locator(NAME).press('Escape');

    // (b) category_tabs: both tiles carry a loaded <img>.
    await setBrowsingMode(page, 'category_tabs');
    await page.goto('/');
    const tiles = page.locator('#browsing-category-tiles');
    const iconTile = tiles.locator('.category-tile', { hasText: s.iconCat });
    const photoTile = tiles.locator('.category-tile', { hasText: s.photoCat });
    await expect(iconTile.locator('img.category-tile-img')).toHaveAttribute('src', /\/public\/assets\/category-icons\/coffee\.svg/);
    await expect(photoTile.locator('img.category-tile-img')).toHaveAttribute('src', new RegExp(`/public/assets/categories/${s.photoCatId}/thumb\\.png`));
    await photoTile.scrollIntoViewIfNeeded();
    await expect.poll(() => imgLoaded(page, `#browsing-category-tiles .category-tile:has-text("${s.photoCat}") img`)).toBe(true);
    await iconTile.scrollIntoViewIfNeeded();
    await expect.poll(() => imgLoaded(page, `#browsing-category-tiles .category-tile:has-text("${s.iconCat}") img`)).toBe(true);
    // A category with no image stays name-only (no broken <img>).
    const plainTiles = tiles.locator('.category-tile:not(:has(img))');
    expect(await plainTiles.count()).toBeGreaterThan(0);

    // Strip mode: the tab and the overflow sheet tile carry the image too.
    // Quick buttons make the categories appear on the strip.
    for (const [i, id] of s.itemIds.entries()) {
      const resp = await page.request.post('/api/buttons/add', { form: { itemId: id, label: `Img2500 QB${i} ${RUN}`, code: `IMG2500-${i}-${RUN}` } });
      expect(resp.ok()).toBe(true);
    }
    try {
      await setBrowsingMode(page, 'strip_overflow');
      await page.goto('/');
      await expect(page.locator(`#cat-tab-${s.iconCatId} img.tab-cat-img`)).toHaveAttribute('src', /coffee\.svg/);
      await expect(page.locator(`#cat-tab-${s.photoCatId} img.tab-cat-img`)).toHaveCount(1);
      await expect(page.locator(`#category-overflow-dialog .category-overflow-tile:has-text("${s.iconCat}") img.category-tile-img`)).toHaveCount(1);
    } finally {
      for (let i = 0; i < s.itemIds.length; i++) {
        await page.request.post('/api/buttons/remove', { form: { code: `IMG2500-${i}-${RUN}` } });
      }
    }

    // (a) "No image" clears it: back on /categories, pick none, Save.
    await page.goto('/categories');
    await row(page, s.photoCat).click();
    await page.locator(`${ICONS} [data-icon="none"]`).click();
    await expect(page.locator('#category-image-current')).toBeHidden();
    await save(page);
    await expect(row(page, s.photoCat)).toHaveAttribute('data-image', '');

    assertClean();
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 800 }]) {
    test(`(c) compact tiles at ${vp.width}x${vp.height}: no taller than an item tile, never under 44px`, async ({ page }) => {
      const assertClean = watchConsole(page);
      seeded = await seed(page);
      const s = seeded;
      await page.setViewportSize(vp);
      await setBrowsingMode(page, 'category_tabs');
      await page.goto('/');
      const tiles = page.locator('#browsing-category-tiles .category-tile');
      await expect(tiles.first()).toBeVisible();

      const heights = await tiles.evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
      const withImg = page.locator('#browsing-category-tiles .category-tile', { hasText: s.iconCat });
      const imgTileH = (await withImg.boundingBox())!.height;

      // An item tile from the same category's popup (the item grid the
      // operator sees right after tapping the tile).
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/buttons/category?id=')),
        withImg.click(),
      ]);
      const itemTile = page.locator('#category-items-modal-body .btn-tile').first();
      await expect(itemTile).toBeVisible();
      const itemH = (await itemTile.boundingBox())!.height;
      await page.locator('#category-items-modal').getByRole('button', { name: 'Close' }).click();

      for (const h of heights) {
        expect(h, `category tile ${h}px must meet the 44px touch floor`).toBeGreaterThanOrEqual(44);
        expect(h, `category tile ${h}px must be no taller than an item tile (${itemH}px)`).toBeLessThanOrEqual(itemH + 0.5);
      }
      expect(imgTileH, 'a tile WITH an image stays no taller than an item tile').toBeLessThanOrEqual(itemH + 0.5);
      // No horizontal page scroll at either size.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow).toBeLessThanOrEqual(1);
      assertClean();
    });
  }
});
