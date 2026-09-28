import { test, expect } from './fixtures';
import type { APIRequestContext, BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';

// ut-docs#2320 (owner decision, implemented by ut-docs#2613): a shop with
// NO categories renders its quick buttons flat -- no tab bar at all, not even
// a lone "Uncategorized" tab -- and the moment one real category exists next
// to uncategorised buttons the strip appears (that category's tab plus the
// uncategorised one). The Go side is pinned by
// TestButtonsHTTPList_FlatWhenNoCategoriesConfigured and
// TestButtonsHTTPList_TabBarWhenCategoryAndUncategorizedButtonCoexist; this
// drives the same two states in a real browser, where Alpine's x-show/tab
// wiring and the strip's overflow measuring actually run.
//
// Why its own till: every default-project worker till is seeded with the
// demo catalogue, which ships four categories -- there is no "zero
// categories" state to reach there, and no honest way back from deleting them
// (other specs on the same worker scan the demo barcodes). So this file boots
// ONE throwaway till with no seeds at all (worker-till.ts `fresh`: its own
// port band and data dir, torn down in afterAll) and runs both cases against
// it in order, serially: the flat state first, then a category is created.
// Nothing here writes to the shared worker till (the ./fixtures auto
// fixtures still boot and reset it, as for every spec). The fresh till gets
// the default `strip_overflow` browsing mode, the only mode with a tab bar
// (buttons.html's `strip_overflow` branch), so that is the mode covered here.
test.describe.configure({ mode: 'serial' });

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

const RUN = Date.now().toString(36).toUpperCase();
const LOOSE_ITEM = `Flat2320 Loose ${RUN}`;
const CAT_NAME = `Flat2320 Cat ${RUN}`;
const CAT_ITEM = `Flat2320 Catted ${RUN}`;

async function createItem(request: APIRequestContext, name: string, categoryId?: string): Promise<void> {
  const form: Record<string, string> = { name, price: '150' };
  if (categoryId) form.categoryId = categoryId;
  const resp = await request.post('/api/catalog/item', { form });
  expect(resp.ok(), `create item ${name} (status ${resp.status()})`).toBe(true);
}

async function itemId(p: Page, name: string): Promise<string> {
  await p.goto('/catalog');
  const row = p.locator(`.catalog-row[data-name="${name}"]`);
  await expect(row, `catalog row for ${name}`).toHaveCount(1);
  return (await row.first().getAttribute('data-id'))!;
}

async function addQuickButton(request: APIRequestContext, id: string, label: string): Promise<void> {
  const resp = await request.post('/api/buttons/add', { form: { itemId: id, label, code: id } });
  expect(resp.ok(), `add quick button ${label} (status ${resp.status()})`).toBe(true);
}

test.beforeAll(async ({ browser }, workerInfo) => {
  till = await startWorkerTill(workerInfo.parallelIndex, { fresh: true });
  context = await browser.newContext({ baseURL: till.url });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

test('zero categories: quick buttons render flat, with no tab bar (ut-docs#2320)', async () => {
  const done = watchConsole(page);

  // Precondition: this really is a shop with no categories.
  await page.goto('/categories');
  await expect(page.locator('.category-row')).toHaveCount(0);

  await createItem(page.request, LOOSE_ITEM);
  await addQuickButton(page.request, await itemId(page, LOOSE_ITEM), LOOSE_ITEM);

  await page.goto('/');
  const grid = page.locator('.products');
  await expect(grid).toBeVisible();
  await expect(grid.locator('.btn-tile', { hasText: LOOSE_ITEM })).toBeVisible();
  await expect(grid.locator('.tab-bar')).toHaveCount(0);
  await expect(grid.locator('[data-cat-tab]')).toHaveCount(0);

  done();
});

test('one category alongside an uncategorised quick button: the tab bar appears with two tabs (ut-docs#2320)', async () => {
  const done = watchConsole(page);

  const created = await page.request.post('/api/categories', { form: { name: CAT_NAME }, maxRedirects: 0 });
  expect([200, 303], `create category (status ${created.status()})`).toContain(created.status());
  await page.goto('/categories');
  const row = page.locator(`.category-row[data-field-name="${CAT_NAME}"]`);
  await expect(row).toHaveCount(1);
  const catId = (await row.getAttribute('data-id'))!;

  await createItem(page.request, CAT_ITEM, catId);
  await addQuickButton(page.request, await itemId(page, CAT_ITEM), CAT_ITEM);

  await page.goto('/');
  const grid = page.locator('.products');
  await expect(grid.locator('.tab-bar')).toBeVisible();
  const tabs = grid.locator('.tab-bar [data-cat-tab]');
  await expect(tabs).toHaveCount(2);
  await expect(grid.locator('#cat-tab-' + catId)).toHaveText(CAT_NAME);
  await expect(grid.locator('#cat-tab-uncategorized')).toBeVisible();

  // The uncategorised button is still reachable: its own tab shows it.
  await grid.locator('#cat-tab-uncategorized').click();
  await expect(grid.locator('.btn-tile', { hasText: LOOSE_ITEM })).toBeVisible();

  done();
});
