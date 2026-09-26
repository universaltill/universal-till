import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2765: an OPEN sale screen picks up a catalog change made outside
// its own document -- here a second browser context standing in for another
// till/tab -- within a few seconds, with no tap and no reload.
//
// HX-Trigger: buttons-changed only ever reached the document that made the
// change. web/public/sell-screen-watch.js now polls GET /ui/buttons/version
// (catalog-only counter, migrations 042/047) while the page is visible and,
// when it differs from the X-UT-Sell-Version the grid was rendered at, fires
// buttons-changed on this document's own <body>. The Go tests prove the
// counter and the header; only a real browser can prove the watcher (htmx
// events, visibility, the defer rules).

const RUN = Date.now().toString(36).toUpperCase();
type Seeded = { itemId: string; name: string; category: string };

async function seedTile(page: Page, tag: string, category: string): Promise<Seeded> {
  const name = `Live2765 ${tag} ${RUN}`;
  const barcode = `LIVE2765-${tag}-${RUN}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},L2765${tag}${RUN},${barcode},1.00,${category},1\n`;
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-live2765-${tag}-${RUN}.csv`,
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
  const add = await page.request.post('/api/buttons/add', { form: { itemId, label: name, code: barcode } });
  expect(add.ok(), 'add quick button').toBe(true);
  return { itemId, name, category };
}

async function openSaleScreenOn(page: Page, category: string) {
  await page.goto('/');
  // The category may sit in the overflow menu on a crowded shared DB, so
  // select its tab directly (Alpine's own @click), as sell-stale-tile-2525 does.
  await page.locator('[data-cat-tab]', { hasText: category }).evaluate((el) => (el as HTMLElement).click());
}

// Counts GETs of the grid itself (/ui/buttons exactly, not /version,
// /category, /search or /all/more).
function countGridFetches(page: Page): () => number {
  let n = 0;
  page.on('request', (req) => {
    if (req.method() === 'GET' && new URL(req.url()).pathname === '/ui/buttons') n++;
  });
  return () => n;
}

test.describe('ut-docs#2765 open sale screen live catalog refresh', () => {
  // Real poll intervals (5 s) are waited out on purpose, several times over.
  test.describe.configure({ timeout: 90_000 });
  const seeded: Seeded[] = [];

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset').catch(() => {});
    for (const s of seeded) {
      await page.request.post('/api/catalog/item/deactivate', { form: { id: s.itemId } }).catch(() => {});
    }
    seeded.length = 0;
  });

  test('a deactivate from another context removes the tile within seconds, without a reload', async ({ page, browser, baseURL }) => {
    const category = `Live2765 Cat ${RUN}`;
    const keep = await seedTile(page, 'K', category);
    const gone = await seedTile(page, 'G', category);
    seeded.push(keep, gone);

    await openSaleScreenOn(page, category);
    await expect(page.locator('.btn-tile:visible', { hasText: gone.name })).toBeVisible();
    await expect(page.locator('.btn-tile:visible', { hasText: keep.name })).toBeVisible();
    const assertClean = watchConsole(page);
    const gridFetches = countGridFetches(page);
    // Survives only if the document is never reloaded/replaced.
    await page.evaluate(() => { (window as unknown as { __ut2765: string }).__ut2765 = 'alive'; });
    const navsBefore = await page.evaluate(() => performance.getEntriesByType('navigation').length);

    // "Another till": a separate browser context with its own sale screen.
    const other = await browser.newContext({ baseURL });
    try {
      const pageB = await other.newPage();
      await openSaleScreenOn(pageB, category);
      const off = await pageB.request.post('/api/catalog/item/deactivate', { form: { id: gone.itemId } });
      expect(off.ok(), 'deactivate from the other context').toBe(true);
    } finally {
      await other.close();
    }

    // No tap, no reload on page A: the watcher (5 s poll) refreshes the grid.
    await expect(page.locator('.btn-tile', { hasText: gone.name })).toHaveCount(0, { timeout: 12_000 });
    await expect(page.locator('.btn-tile:visible', { hasText: keep.name })).toBeVisible();
    expect(await page.evaluate(() => (window as unknown as { __ut2765?: string }).__ut2765)).toBe('alive');
    expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navsBefore);
    expect(gridFetches(), 'grid refetched exactly by the watcher').toBeGreaterThanOrEqual(1);
    // The selected category survived the refresh (restoreGridState).
    await expect(page.locator('[data-cat-tab][aria-selected="true"]', { hasText: category })).toHaveCount(1);
    assertClean();
  });

  test('waits while a search is open, and a completed sale never triggers a refresh', async ({ page, browser, baseURL }) => {
    const category = `Live2765 Cat S ${RUN}`;
    const keep = await seedTile(page, 'SK', category);
    const gone = await seedTile(page, 'SG', category);
    seeded.push(keep, gone);

    await openSaleScreenOn(page, category);
    await expect(page.locator('.btn-tile:visible', { hasText: gone.name })).toBeVisible();
    const assertClean = watchConsole(page);
    const gridFetches = countGridFetches(page);

    // A completed sale elsewhere does not move the catalog-only counter,
    // so the grid is left alone.
    const other = await browser.newContext({ baseURL });
    try {
      const req = other.request;
      const scan = await req.post('/api/pos/scan', { form: { code: `LIVE2765-SK-${RUN}` } });
      expect(scan.ok(), 'scan on the other context').toBe(true);
      const tender = await req.post('/api/pos/tender', {
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        data: { payments: [{ method: 'cash', amount: 100000 }], offline: true },
      });
      expect(tender.ok(), `tender (${tender.status()}): ${await tender.text()}`).toBe(true);
      await page.waitForTimeout(7_000); // > one 5 s poll interval
      expect(gridFetches(), 'a completed sale made the open grid refetch').toBe(0);

      // Open the sale-screen search, then change the catalog elsewhere: the
      // refresh must wait while searching...
      await page.locator('.products-finder [x-ref="searchBtn"]').click();
      await expect(page.locator('#products-search')).toBeVisible();
      const off = await req.post('/api/catalog/item/deactivate', { form: { id: gone.itemId } });
      expect(off.ok(), 'deactivate from the other context').toBe(true);
      await page.waitForTimeout(7_000);
      expect(gridFetches(), 'the grid refreshed under an open search').toBe(0);
    } finally {
      await other.close();
    }

    // ...and happen once the search is closed.
    await page.locator('.products-strip-back').click();
    await expect(page.locator('#products-search')).toBeHidden();
    await expect(page.locator('.btn-tile', { hasText: gone.name })).toHaveCount(0, { timeout: 12_000 });
    await expect(page.locator('.btn-tile:visible', { hasText: keep.name })).toBeVisible();
    assertClean();
  });
});
