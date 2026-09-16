import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2283: an optional, settings-gated "Categories" tab — first among
// the sell screen's tabs — showing a grid of category tiles; tapping one
// opens a modal item-picker for that category's own items, populated by
// cloning that category's already-rendered tiles (see buttons.html's
// openCategoryPicker comment), not a second fetch/render.
//
// Per-run suffix, same convention sell-tile-long-press-2285.spec.ts already
// uses: cleanupItems() deactivates fixture items, and a deactivated item no
// longer renders a .catalog-row, so a second local re-run against the same
// still-running till could not re-seed under the same SKU without this.
//
// A further per-TEST tag (T1/T2/T3) is folded into the suffix too — found
// live, not assumed: three tests in this one file each call seedItems, and
// this file's own afterEach deactivates every item after EACH test (it has
// to — a settings-gated tab left ON, or a stray shortcut, must not leak
// into whichever spec file the runner picks next). Reusing the exact same
// name/SKU/barcode across tests within this one file hits that exact
// already-deactivated-row gap the comment above describes, just one test
// run sooner than a second LOCAL RUN of the whole file — confirmed by a
// real timeout (seedItems' own catalog-row lookup) when this file's tests
// shared one item set, gone once each test's set is disjoint.
type Item = { name: string; sku: string; barcode: string; category: string };
function makeItems(tag: string, run: string) {
  const catA = `Sheet2283 Cat A ${run}${tag}`;
  const catB = `Sheet2283 Cat B ${run}${tag}`;
  const plain: Item = { name: `Sheet2283 Plain ${run}${tag}`, sku: `SHEET2283P${run}${tag}`, barcode: `SHEET2283BC-P-${run}${tag}`, category: catA };
  const mod: Item = { name: `Sheet2283 Mod ${run}${tag}`, sku: `SHEET2283M${run}${tag}`, barcode: `SHEET2283BC-M-${run}${tag}`, category: catA };
  const other: Item = { name: `Sheet2283 Other ${run}${tag}`, sku: `SHEET2283O${run}${tag}`, barcode: `SHEET2283BC-O-${run}${tag}`, category: catB };
  return { catA, catB, plain, mod, other, all: [plain, mod, other] };
}
const RUN = Date.now().toString(36).toUpperCase();

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Mirrors sell-tile-long-press-2285.spec.ts's own seedItems: a plain
// catalog import only creates `items` rows (the sale-screen grid groups
// SHORTCUT buttons, not catalog items directly), so each item is ALSO
// added as a sell-screen shortcut via /api/buttons/add so it actually
// renders as a tile. Returns each item's catalog id, keyed by name — the
// modifier group below needs ITEM_MOD's real id.
async function seedItems(page: Page, items: Item[]): Promise<Record<string, string>> {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-2283-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(csvFor(items)),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  const ids: Record<string, string> = {};
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    const id = (await row.first().getAttribute('data-id'))!;
    ids[it.name] = id;
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id, label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
  return ids;
}

// Attaches a real, minimal modifier group + option to itemId directly
// through the catalog API — no UI round trip needed (osk-decimal-sale-
// catalog-fields-1284.spec.ts drives the full #modifier-groups-modal UI
// for its own different purpose; this test only needs a real modifier item
// to exist so ButtonStore.Load's HasModifiers flag is genuinely true, not
// asserted).
async function seedModifier(page: Page, itemId: string, optionName: string): Promise<void> {
  const groupName = `Size ${optionName}`; // unique per call — see optionName's own doc below
  const groupResp = await page.request.post('/api/catalog/modifier-group', {
    form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
  });
  expect(groupResp.ok(), 'create modifier group').toBe(true);

  // The group id isn't in that POST's own response (it answers with the
  // item's #catalog-variants fragment by default, no Hx-Target header —
  // see handlers.go's renderModifierMutationResult) — read it back from
  // the modifier-groups-panel fragment instead, same shape
  // modifier_group_admin.html renders it in: a hidden name="id" input
  // right before the group's own name="name" input on the same form.
  const panelResp = await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`);
  expect(panelResp.ok(), 'fetch modifier groups panel').toBe(true);
  const html = await panelResp.text();
  const nameIdx = html.indexOf(`value="${groupName}"`);
  expect(nameIdx, 'modifier-groups-panel must contain the new group').toBeGreaterThan(-1);
  const idMatches = [...html.slice(0, nameIdx).matchAll(/name="id" value="([^"]*)"/g)];
  expect(idMatches.length, 'modifier-groups-panel must expose the new group id').toBeGreaterThan(0);
  const groupId = idMatches[idMatches.length - 1][1];

  // optionName IS the option's own display name — passed in by the caller
  // (not built from the shared RUN suffix here) so it can carry the same
  // per-test tag as the item set this modifier is attached to.
  const optResp = await page.request.post('/api/catalog/modifier-option', {
    form: { groupId, itemId, name: optionName },
  });
  expect(optResp.ok(), 'create modifier option').toBe(true);
}

async function setCategoriesTab(page: Page, enabled: boolean): Promise<void> {
  const resp = await page.request.post('/api/settings/categories-tab', { form: { enabled: String(enabled) } });
  expect(resp.ok(), `set categories-tab=${enabled}`).toBe(true);
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

test.describe('Sell screen: optional Categories tab (ut-docs#2283)', () => {
  // Tracks which items THIS test seeded, for afterEach's own cleanup below
  // — each test builds its own disjoint item set via makeItems (see that
  // function's own comment on why sharing one set across tests broke).
  let seeded: Item[] = [];

  test.afterEach(async ({ page }) => {
    // Never leave the settings-gated tab ON for a later spec file — this
    // key is a persisted DB row, not per-test state like the basket reset
    // fixtures.ts already handles.
    await setCategoriesTab(page, false);
    await cleanupItems(page, seeded);
    seeded = [];
    await page.request.post('/api/pos/reset');
  });

  test('the tab is absent by default and appears/disappears with the setting', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { plain, other } = makeItems('T1', RUN);
    seeded = [plain, other];
    await seedItems(page, seeded);

    await page.goto('/');
    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    await expect(tabBar.getByRole('tab', { name: 'Categories' })).toHaveCount(0);

    await setCategoriesTab(page, true);
    await page.goto('/');
    const categoriesTab = tabBar.getByRole('tab', { name: 'Categories' });
    await expect(categoriesTab).toBeVisible();
    // First of ALL tabs — before the ut-docs#2212 All tab too, not just
    // before the per-category ones.
    await expect(tabBar.locator('.tab').first()).toHaveId('cat-tab-categories');

    await setCategoriesTab(page, false);
    await page.goto('/');
    await expect(tabBar.getByRole('tab', { name: 'Categories' })).toHaveCount(0);

    assertClean();
  });

  test('tapping a category tile opens a picker with exactly that category\'s items, and a modifier item still opens its own picker', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { catA, plain, mod, other, all } = makeItems('T2', RUN);
    seeded = all;
    const ids = await seedItems(page, all);
    const optionName = `Large T2 ${RUN}`;
    await seedModifier(page, ids[mod.name], optionName);
    await setCategoriesTab(page, true);

    await page.goto('/');
    const tabBar = page.locator('.products .tab-bar');
    await tabBar.getByRole('tab', { name: 'Categories' }).click();

    const tile = page.locator('.category-tile', { hasText: catA });
    await expect(tile).toBeVisible();

    const modal = page.locator('#category-items-modal');
    await tile.click();
    await expect(modal).toBeVisible();
    await expect(page.locator('#category-items-modal-name')).toHaveText(catA);

    // Exactly the two CAT_A items — never the CAT_B one, and never a
    // second-level modal-within-modal (the card explicitly rules that out).
    const modalBody = page.locator('#category-items-modal-body');
    await expect(modalBody.locator('.btn-tile', { hasText: plain.name })).toBeVisible();
    await expect(modalBody.locator('.btn-tile', { hasText: mod.name })).toBeVisible();
    await expect(modalBody.locator('.btn-tile', { hasText: other.name })).toHaveCount(0);

    // A plain (non-modifier) tile inside the modal still adds straight to
    // the basket — proving the clone's hx-post was actually re-wired by
    // htmx.process(), not silently inert (the exact failure mode
    // ut-docs' list-and-dialog-pattern.md's "hx-boost reads action ONCE"
    // section warns about for a mutated/cloned element).
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      modalBody.locator('.btn-tile', { hasText: plain.name }).click(),
    ]);
    await expect(page.locator('#basket')).toContainText(plain.name);

    // The modifier tile inside the modal must open the REAL modifier
    // picker (#modifier-modal), not silently no-op and not just add the
    // base item straight to the basket.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
      modalBody.locator('.btn-tile', { hasText: mod.name }).click(),
    ]);
    const modifierModal = page.locator('#modifier-modal');
    await expect(modifierModal).toBeVisible();
    await expect(modifierModal).toContainText(mod.name);
    await modifierModal.locator('.modifier-option', { hasText: optionName }).locator('input').check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers')),
      modifierModal.getByRole('button', { name: /Add to cart/i }).click(),
    ]);
    await expect(page.locator('#basket')).toContainText(mod.name);
    await expect(page.locator('#basket')).toContainText(optionName);

    assertClean();
  });

  test('category tiles are at least as large as product tiles, at the kiosk floor and at phone width', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { plain, other } = makeItems('T3', RUN);
    seeded = [plain, other];
    await seedItems(page, seeded);
    await setCategoriesTab(page, true);

    for (const viewport of [
      { width: 1024, height: 600 }, // kiosk floor
      { width: 360, height: 740 }, // phone width
    ]) {
      await page.setViewportSize(viewport);
      await page.goto('/');

      const tabBar = page.locator('.products .tab-bar');
      const allTab = tabBar.getByRole('tab', { name: 'All' });
      await expect(allTab).toBeVisible();
      const productTile = page.locator('.btn-tile').first();
      await expect(productTile).toBeVisible();
      const productBox = (await productTile.boundingBox())!;

      await tabBar.getByRole('tab', { name: 'Categories' }).click();
      const categoryTile = page.locator('.category-tile').first();
      await expect(categoryTile).toBeVisible();
      const categoryBox = (await categoryTile.boundingBox())!;

      expect(
        categoryBox.height,
        `at ${viewport.width}x${viewport.height}, category tile height ${categoryBox.height} must be >= product tile height ${productBox.height}`,
      ).toBeGreaterThanOrEqual(productBox.height - 1); // -1px: sub-pixel rounding tolerance
      expect(
        categoryBox.width,
        `at ${viewport.width}x${viewport.height}, category tile width ${categoryBox.width} must be >= product tile width ${productBox.width}`,
      ).toBeGreaterThanOrEqual(productBox.width - 1);
    }

    assertClean();
  });
});
