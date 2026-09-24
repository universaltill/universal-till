import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#2499: ONE setting (Settings → Sell screen, sale.browsing_mode)
// picks how the sell screen browses the catalog:
//   category_tabs    — a grid of category tiles; tapping one opens a popup
//                      listing EVERY active item in that category (not just
//                      its quick buttons — ut-docs#2372, absorbed) with its
//                      own search box;
//   all_filter_chips — the All grid with a row of category filter chips;
//   strip_overflow   — the quick-button category strip with the "…"
//                      overflow (ut-docs#2307's own spec covers it deeply;
//                      this file only pins that it is now reached via the
//                      setting).
// Search works in every mode; the strip's jiggle edit (ut-docs#2339) is
// untouched, and the All grid still never arms it (app.js's inAllGrid()).
//
// This file replaced sell-screen-categories-tab-2283.spec.ts: the settings-
// gated Categories TAB it covered is gone — the category_tabs mode IS that
// tile grid, as the whole view — and its clone-the-quick-button-panel
// picker was exactly what #2372 asked to replace.
//
// HONESTY NOTE: chips and tiles are driven with Playwright's synthetic
// mouse/keyboard in Chromium. The 44px touch floor, wrap-never-clip and
// aria-pressed/checkmark state are asserted for real below; how they feel
// under a finger on the pilot till's WebKitGTK kiosk is the local hardware
// lane's to confirm, same convention as sell-tile-jiggle-mode-2339.spec.ts.

type Item = { name: string; sku: string; barcode: string; category: string; quickButton: boolean };
const RUN = Date.now().toString(36).toUpperCase();
function makeItems(tag: string) {
  const run = `${RUN}${tag}`;
  const catA = `Browse2499 Cat A ${run}`;
  const catB = `Browse2499 Cat B ${run}`;
  const plain: Item = { name: `Browse2499 Plain ${run}`, sku: `BR2499P${run}`, barcode: `BR2499BC-P-${run}`, category: catA, quickButton: true };
  const mod: Item = { name: `Browse2499 Mod ${run}`, sku: `BR2499M${run}`, barcode: `BR2499BC-M-${run}`, category: catA, quickButton: true };
  // The whole point of ut-docs#2372: an active item with NO quick button.
  const noqb: Item = { name: `Browse2499 Zzz NoButton ${run}`, sku: `BR2499N${run}`, barcode: `BR2499BC-N-${run}`, category: catA, quickButton: false };
  const other: Item = { name: `Browse2499 Other ${run}`, sku: `BR2499O${run}`, barcode: `BR2499BC-O-${run}`, category: catB, quickButton: true };
  return { catA, catB, plain, mod, noqb, other, all: [plain, mod, noqb, other] };
}

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Same import-then-add-shortcut shape sell-tile-jiggle-mode-2339.spec.ts
// uses, except an item flagged quickButton:false is left as a plain
// catalog row — which is what the category popup must still list.
async function seedItems(page: Page, items: Item[]): Promise<Record<string, string>> {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-2499-${RUN}.csv`,
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
    if (!it.quickButton) continue;
    const resp = await page.request.post('/api/buttons/add', { form: { itemId: id, label: it.name, code: it.barcode } });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
  return ids;
}

// Minimal real modifier group + option, straight through the catalog API
// (same helper shape the retired 2283 spec used).
async function seedModifier(page: Page, itemId: string, optionName: string): Promise<void> {
  const groupName = `Size ${optionName}`;
  const groupResp = await page.request.post('/api/catalog/modifier-group', {
    form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
  });
  expect(groupResp.ok(), 'create modifier group').toBe(true);
  const panelResp = await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`);
  expect(panelResp.ok(), 'fetch modifier groups panel').toBe(true);
  const html = await panelResp.text();
  const nameIdx = html.indexOf(`>${groupName}<`);
  expect(nameIdx, 'modifier-groups-panel must contain the new group').toBeGreaterThan(-1);
  const idMatches = [...html.slice(0, nameIdx).matchAll(/data-group-id="([^"]*)"/g)];
  expect(idMatches.length, 'modifier-groups-panel must expose the new group id').toBeGreaterThan(0);
  const groupId = idMatches[idMatches.length - 1][1];
  const optResp = await page.request.post('/api/catalog/modifier-option', { form: { groupId, itemId, name: optionName } });
  expect(optResp.ok(), 'create modifier option').toBe(true);
}

async function cleanupItems(page: Page, items: Item[]) {
  for (const it of items) {
    if (it.quickButton) await page.request.post('/api/buttons/remove', { form: { code: it.barcode } });
  }
  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

// Many categories, each with one active item and NO quick button — the
// chip mode browses the catalog itself, so a category needs no button to
// earn a chip. Same create-through-the-API-then-read-the-id-back shape as
// sale-screen-category-strip-overflow-2307.spec.ts's own helper.
type Cat = { id: string; name: string; itemId: string; itemName: string };
async function createChipCategories(page: Page, tag: string, count: number): Promise<Cat[]> {
  const catNames = Array.from({ length: count }, (_, i) => `Chip2499 ${tag} ${RUN} ${String(i + 1).padStart(2, '0')}`);
  for (const name of catNames) {
    const resp = await page.request.post('/api/categories', { form: { name }, maxRedirects: 0 });
    expect([200, 303], `create category ${name}`).toContain(resp.status());
  }
  await page.goto('/categories');
  const catIds: string[] = [];
  for (const name of catNames) {
    const row = page.locator(`.category-row[data-field-name="${name}"]`);
    await expect(row, `category row for ${name}`).toHaveCount(1);
    catIds.push((await row.getAttribute('data-id'))!);
  }
  const itemNames = catNames.map((n) => `${n} Item`);
  for (let i = 0; i < count; i++) {
    const resp = await page.request.post('/api/catalog/item', { form: { name: itemNames[i], price: '150', categoryId: catIds[i] } });
    expect(resp.ok(), `create item ${itemNames[i]}`).toBe(true);
  }
  await page.goto('/catalog');
  const itemIds: string[] = [];
  for (const name of itemNames) {
    const row = page.locator(`.catalog-row[data-name="${name}"]`);
    await expect(row, `catalog row for ${name}`).toHaveCount(1);
    itemIds.push((await row.first().getAttribute('data-id'))!);
  }
  return catNames.map((name, i) => ({ id: catIds[i], name, itemId: itemIds[i], itemName: itemNames[i] }));
}

async function deactivate(page: Page, cats: Cat[]): Promise<void> {
  for (const c of cats) await page.request.post('/api/catalog/item/deactivate', { form: { id: c.itemId } });
}

test.describe('Sell screen browsing mode (ut-docs#2499)', () => {
  let seeded: Item[] = [];
  let chipCats: Cat[] = [];

  test.afterEach(async ({ page }) => {
    // Persisted till state: never leave another mode behind for the next
    // spec file on this worker (worker-till.ts boots in strip_overflow).
    await setBrowsingMode(page, 'strip_overflow');
    if (seeded.length) await cleanupItems(page, seeded);
    seeded = [];
    if (chipCats.length) await deactivate(page, chipCats);
    chipCats = [];
    await page.request.post('/api/pos/reset');
  });

  test('Settings → Sell screen: a select with three named modes, a helper line that follows the selection, persisted on Apply', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/settings#settings-sell-screen');
    const form = page.locator('#browsing-mode-form');
    const select = form.locator('select[name=mode]');
    await expect(select).toHaveValue('strip_overflow'); // the worker till's own mode
    const hint = (mode: string) => form.locator(`[data-browsing-mode-hint="${mode}"]`);
    await expect(hint('strip_overflow')).toBeVisible();
    await expect(hint('category_tabs')).toBeHidden();
    await expect(hint('all_filter_chips')).toBeHidden();
    // Human-readable option labels, not enum values (i18n keys resolve).
    await expect(select.locator('option[value=category_tabs]')).toHaveText('Category tiles');
    await expect(select.locator('option[value=all_filter_chips]')).toHaveText('All items with category filters');
    await expect(select.locator('option[value=strip_overflow]')).toHaveText('Category strip with quick buttons');

    // The helper line explains the choice BEFORE Apply is pressed.
    await select.selectOption('all_filter_chips');
    await expect(hint('all_filter_chips')).toBeVisible();
    await expect(hint('strip_overflow')).toBeHidden();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/browsing-mode') && r.status() === 204),
      form.locator('button[type=submit]').click(),
    ]);
    await expect(form.locator('#browsing-mode-msg')).toContainText('Saved');
    await page.reload();
    await expect(page.locator('#browsing-mode-form select[name=mode]')).toHaveValue('all_filter_chips');
    await expect(page.locator('#browsing-mode-form [data-browsing-mode-hint="all_filter_chips"]')).toBeVisible();

    // And the sell screen actually follows it.
    await page.goto('/');
    await expect(page.locator('#browsing-category-chips')).toBeVisible();
    await expect(page.locator('.products .tab-bar')).toHaveCount(0);

    assertClean();
  });

  test('category_tabs: a tile per category; the popup lists every active item (not just quick buttons) with its own search, and sells from it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { catA, catB, plain, mod, noqb, other, all } = makeItems('T2');
    seeded = all;
    const ids = await seedItems(page, all);
    const optionName = `Large T2 ${RUN}`;
    await seedModifier(page, ids[mod.name], optionName);
    await setBrowsingMode(page, 'category_tabs');

    await page.goto('/');
    const tiles = page.locator('#browsing-category-tiles');
    await expect(tiles).toBeVisible();
    await expect(page.locator('.products .tab-bar')).toHaveCount(0);
    await expect(page.locator('#browsing-category-chips')).toHaveCount(0);
    // No quick-button tile on the main view — nothing for jiggle to arm on.
    await expect(page.locator('#buttons-grid .btn-tile')).toHaveCount(0);
    const tileA = tiles.locator('.category-tile', { hasText: catA });
    await expect(tileA).toBeVisible();
    await expect(tiles.locator('.category-tile', { hasText: catB })).toBeVisible();
    // The count on the tile is ITEMS: catA has three active items, only
    // two of them quick buttons.
    await expect(tileA).toContainText('3 item(s)');

    const modal = page.locator('#category-items-modal');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/buttons/category?id=')),
      tileA.click(),
    ]);
    await expect(modal).toBeVisible();
    await expect(page.locator('#category-items-modal-name')).toHaveText(catA);
    const body = page.locator('#category-items-modal-body');
    await expect(body.locator('.btn-tile', { hasText: plain.name })).toBeVisible();
    await expect(body.locator('.btn-tile', { hasText: mod.name })).toBeVisible();
    // ut-docs#2372's whole complaint: an item with no quick button IS listed.
    await expect(body.locator('.btn-tile', { hasText: noqb.name })).toBeVisible();
    await expect(body.locator('.btn-tile', { hasText: other.name })).toHaveCount(0);
    // Quick buttons first, then the rest A–Z: the no-button item sorts
    // last by name AND by rule, so it must come after both quick buttons.
    const names = await body.locator('.btn-tile .tile-name').allTextContents();
    expect(names.indexOf(noqb.name)).toBeGreaterThan(names.indexOf(plain.name));
    expect(names.indexOf(noqb.name)).toBeGreaterThan(names.indexOf(mod.name));

    // The popup's own search narrows the list in place.
    const search = body.locator('.category-items-search');
    await expect(search).toBeVisible();
    await search.fill('NoButton');
    await expect(body.locator('.btn-tile', { hasText: noqb.name })).toBeVisible();
    await expect(body.locator('.btn-tile', { hasText: plain.name })).toBeHidden();
    await search.fill('zzzz-nothing-matches');
    await expect(body.locator('.empty')).toBeVisible();
    await search.fill('');
    await expect(body.locator('.btn-tile', { hasText: plain.name })).toBeVisible();

    // A plain tile in the popup adds straight to the basket; the no-button
    // item sells the same way (it was never a quick button, so this is the
    // resolver path ut-docs#2294 opened for the All tab, reused here).
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      body.locator('.btn-tile', { hasText: noqb.name }).click(),
    ]);
    await expect(page.locator('#basket')).toContainText(noqb.name);

    // A modifier item opens the REAL picker on top of the popup.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
      body.locator('.btn-tile', { hasText: mod.name }).click(),
    ]);
    const modifierModal = page.locator('#modifier-modal');
    await expect(modifierModal).toBeVisible();
    await modifierModal.locator('.modifier-option', { hasText: optionName }).locator('input').check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers')),
      modifierModal.getByRole('button', { name: /Add to cart/i }).click(),
    ]);
    await expect(page.locator('#basket')).toContainText(mod.name);
    await expect(page.locator('#basket')).toContainText(optionName);
    await modal.getByRole('button', { name: 'Close' }).click();
    await expect(modal).toBeHidden();

    // The strip's own search still works in this mode, across categories.
    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill(other.name);
    await expect(page.locator('#search-results .btn-tile', { hasText: other.name })).toBeVisible();
    await expect(tiles).toBeHidden();
    await page.locator('.products-strip-back').click();
    await expect(tiles).toBeVisible();

    assertClean();
  });

  // ut-docs#2499 (Tester finding): the popup is a non-modal <dialog>
  // (.show(), never .showModal() -- see buttons.html's own comment on why:
  // the OSK must stay reachable for the popup's search box). Every OTHER
  // non-showModal dialog in this codebase (#hold-modal, #pfand-modal,
  // #elevation-modal, #table-add-modal, .category-overflow-dialog) carries
  // its own explicit `position: fixed; z-index: 500` for exactly that
  // reason -- a non-modal dialog gets none of showModal()'s free top-layer
  // stacking, so without it the dialog falls back to the UA default
  // (position: absolute, z-index: auto) and can end up BEHIND ordinary
  // page content once the layout collapses to one column. Confirmed live
  // at 360x800: the sell screen's own "Add items to pay" button rendered
  // ON TOP of an item tile inside the open popup, both visually (a real
  // screenshot) and to elementFromPoint (a tap there would hit the page
  // behind the popup instead of the popup itself). Desktop/kiosk widths
  // (1024x600, 1280x720) don't reproduce it -- the two-column layout keeps
  // the dialog's absolute-positioned box clear of the tender column by
  // coincidence, not by any rule -- so this asserts the one width that
  // does.
  test('category_tabs popup stacks above page content at phone width (no bleed-through)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await setBrowsingMode(page, 'category_tabs');
    await page.goto('/');
    const tile = page.locator('#browsing-category-tiles .category-tile').first();
    await expect(tile).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/buttons/category?id=')),
      tile.click(),
    ]);
    const modal = page.locator('#category-items-modal');
    await expect(modal).toBeVisible();
    // Sample every corner plus the centre of the dialog's own box (clamped
    // to the viewport, since the dialog's max-height can exceed a short
    // phone screen) -- every one of them must resolve to the dialog, not
    // whatever page content happens to sit at that point underneath it.
    const results = await page.evaluate(() => {
      const dlg = document.getElementById('category-items-modal')!;
      const r = dlg.getBoundingClientRect();
      const clampX = (x: number) => Math.min(Math.max(x, 0), window.innerWidth - 1);
      const clampY = (y: number) => Math.min(Math.max(y, 0), window.innerHeight - 1);
      const points: [number, number][] = [
        [r.left + r.width / 2, r.top + r.height / 2],
        [r.left + 4, r.top + 4],
        [r.right - 4, r.top + 4],
        [r.left + 4, r.bottom - 4],
        [r.right - 4, r.bottom - 4],
      ];
      return points.map(([x, y]) => {
        const cx = clampX(x);
        const cy = clampY(y);
        const top = document.elementFromPoint(cx, cy);
        return { x: cx, y: cy, tag: top && top.tagName, cls: top && (top as HTMLElement).className, insideDialog: !!(top && top.closest('#category-items-modal')) };
      });
    });
    for (const r of results) {
      expect(r.insideDialog, `point (${r.x},${r.y}) inside the open popup resolved to <${r.tag} class="${r.cls}"> instead of the popup`).toBe(true);
    }
    await modal.getByRole('button', { name: 'Close' }).click();
    assertClean();
  });

  test('all_filter_chips at 1024x600 with 12+ categories: chips wrap (never clip), carry aria-pressed + a checkmark, filter the grid by tap and by keyboard; search still works; the All grid never arms jiggle', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    chipCats = await createChipCategories(page, 'C', 12);
    await setBrowsingMode(page, 'all_filter_chips');

    await page.goto('/');
    const chips = page.locator('#browsing-category-chips');
    await expect(chips).toBeVisible();
    await expect(page.locator('.products .tab-bar')).toHaveCount(0);
    await expect(page.locator('#browsing-category-tiles')).toHaveCount(0);
    const grid = page.locator('#buttons-grid-all');
    await expect(grid).toBeVisible();

    // Never a silent clip or a horizontal scroll (UX gate on ut-docs#2499).
    const rowOverflow = await chips.evaluate((el) => el.scrollWidth - el.clientWidth);
    expect(rowOverflow, 'chip row must wrap, never overflow its own box').toBeLessThanOrEqual(1);
    const pageOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(pageOverflow, 'page must not scroll horizontally').toBeLessThanOrEqual(1);
    for (const c of chipCats) {
      const chip = chips.locator(`.chip[data-cat-id="${c.id}"]`);
      await expect(chip, `chip for ${c.name}`).toBeVisible();
      const box = (await chip.boundingBox())!;
      expect(box.height, `chip ${c.name} must meet the 44px touch floor`).toBeGreaterThanOrEqual(44);
    }

    // The All chip starts pressed, with its checkmark visible; a category
    // chip starts unpressed with the checkmark hidden — state is never
    // colour alone.
    const allChip = chips.locator('[data-cat-all]');
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(allChip.locator('.chip-check')).toBeVisible();
    const target = chipCats[4];
    const targetChip = chips.locator(`.chip[data-cat-id="${target.id}"]`);
    await expect(targetChip).toHaveAttribute('aria-pressed', 'false');
    await expect(targetChip.locator('.chip-check')).toBeHidden();

    // Tap filters the grid server-side (paging stays inside the filter).
    await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/ui/buttons/all/more`) && r.url().includes(`category=${target.id}`)),
      targetChip.click(),
    ]);
    await expect(targetChip).toHaveAttribute('aria-pressed', 'true');
    await expect(targetChip.locator('.chip-check')).toBeVisible();
    await expect(allChip).toHaveAttribute('aria-pressed', 'false');
    await expect(allChip.locator('.chip-check')).toBeHidden();
    await expect(grid.locator('.btn-tile', { hasText: target.itemName })).toBeVisible();
    await expect(grid.locator('.btn-tile', { hasText: chipCats[0].itemName })).toHaveCount(0);
    await expect(grid.locator('.btn-tile', { hasText: 'Coca-Cola' })).toHaveCount(0); // demo catalogue, other category

    // Keyboard: Tab from the pressed chip reaches the next chip, Space
    // presses it (plain <button>s — no gesture code).
    await targetChip.focus();
    await page.keyboard.press('Tab');
    const next = chips.locator(`.chip[data-cat-id="${chipCats[5].id}"]`);
    await expect(next).toBeFocused();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes(`category=${chipCats[5].id}`)),
      page.keyboard.press('Space'),
    ]);
    await expect(next).toHaveAttribute('aria-pressed', 'true');
    await expect(targetChip).toHaveAttribute('aria-pressed', 'false');
    await expect(grid.locator('.btn-tile', { hasText: chipCats[5].itemName })).toBeVisible();

    // Back to All: the whole catalog again.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes('category=all')),
      allChip.click(),
    ]);
    // The catalog item's own name (the All grid lists catalog items; the
    // demo quick button for it is labelled "Coca-Cola 330ml" instead — see
    // sale-screen-category-tabs-search-418.spec.ts).
    await expect(grid.locator('.btn-tile', { hasText: 'Coca-Cola Can 330ml' })).toBeVisible();

    // The All grid never arms jiggle mode (app.js's inAllGrid() guard,
    // ut-docs#2402) — a right-click is the mouse-till entry gesture.
    const anyTile = grid.locator('.btn-tile').first();
    await anyTile.click({ button: 'right' });
    await page.waitForTimeout(300);
    await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);

    // Search still works in this mode.
    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill(target.itemName);
    await expect(page.locator('#search-results .btn-tile', { hasText: target.itemName })).toBeVisible();
    await expect(chips).toBeHidden();
    await page.locator('.products-strip-back').click();
    await expect(chips).toBeVisible();

    assertClean();
  });

  test('strip_overflow: selecting it explicitly renders the quick-button strip (All tab first, no chips, no tiles)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setBrowsingMode(page, 'strip_overflow');
    await page.goto('/');
    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    await expect(tabBar.locator('.tab').first()).toHaveId('cat-tab-all');
    await expect(page.locator('#cat-tab-more')).toHaveCount(1); // hidden while everything fits — see the 2307 spec
    await expect(page.locator('#browsing-category-chips')).toHaveCount(0);
    await expect(page.locator('#browsing-category-tiles')).toHaveCount(0);
    // No trace of the retired settings-gated Categories tab.
    await expect(page.locator('#cat-tab-categories')).toHaveCount(0);
    assertClean();
  });
});
