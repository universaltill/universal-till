import { test, expect } from './fixtures';
import type { Page, Locator, Request } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2174: /designer renders a LIVE REPLICA of the sale screen's
// product panel (the same buttons.html partial, via GET
// /ui/designer/buttons) and edits it in place. The sell screen's own
// jiggle edit mode (ut-docs#2339, app.js utTileJiggle) is the editor,
// extended to the category strip: the strip's pencil toggles it, every
// category tab gains a pencil that opens an inline rename/recolour/remove
// popover, a + tab adds a category, and category tabs drag/arrow-key
// reorder exactly like tiles do, persisted with ONE POST to
// /api/designer/categories/reorder on Done. Every edit re-renders the
// replica via htmx (204 + HX-Trigger: buttons-changed) -- no
// save-then-navigate. All at the pilot till's 1024x600.
//
// HONESTY NOTE (same convention as sell-tile-jiggle-mode-2339.spec.ts):
// every gesture below is Playwright's synthetic mouse pointer in Chromium
// -- real PointerEvents, pointerType "mouse", not real touch hardware and
// not WebKitGTK. What this proves: the markup is the sale screen's, the
// mode toggles, the popover CRUD round-trips through the real server, the
// keyboard and pointer reorder paths persist, the popover stays inside
// the 1024x600 viewport without pushing the grid off screen. What it
// cannot prove: touch-action/pointer-capture behaviour under a real
// finger on the kiosk -- ut-docs#2467 is the hardware lane's card.

const RUN = Date.now().toString(36).toUpperCase();
function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  const catA = `Replica2174 CatA ${run}`;
  const catB = `Replica2174 CatB ${run}`;
  return {
    run,
    catA,
    catB,
    A1: { name: `Replica2174 Item A1 ${run}`, sku: `REP2174A1${run}`, barcode: `REP2174BC-A1-${run}`, category: catA },
    A2: { name: `Replica2174 Item A2 ${run}`, sku: `REP2174A2${run}`, barcode: `REP2174BC-A2-${run}`, category: catA },
    B1: { name: `Replica2174 Item B1 ${run}`, sku: `REP2174B1${run}`, barcode: `REP2174BC-B1-${run}`, category: catB },
  };
}
type Item = ReturnType<typeof fixture>['A1'];

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Same seeding as sell-tile-jiggle-mode-2339.spec.ts: a catalog import
// creates the items (and their categories); each is then also added as a
// quick button so it renders as a tile. `addAsButton` false leaves an item
// catalog-only, which is what the search-add step below needs.
async function seedItems(page: Page, items: Item[], addAsButton = true) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: 'import-2174.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(csvFor(items)),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  if (!addAsButton) return;
  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    const id = (await row.first().getAttribute('data-id'))!;
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id, label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
}

async function cleanup(page: Page, items: Item[], categoryIDs: string[]) {
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
  // Deactivating an item clears the ErrCategoryHasItems guard, so the
  // fixture categories can be deactivated too and never pile up as tabs
  // across local re-runs.
  for (const id of categoryIDs) {
    await page.request.post(`/api/designer/categories/${id}/active`, { form: { active: '0' } });
  }
}

async function center(el: Locator): Promise<{ x: number; y: number }> {
  const box = (await el.boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

// Drag `subject` and drop it in the START half of `over` (an earlier
// sibling) -- the same stepped pointer path the jiggle spec's dragPast
// uses for tiles, mirrored for a backward move: app.js's reorderAt moves
// the dragged cell BEFORE an earlier sibling once the pointer sits on its
// inline-start side of the midpoint.
async function dragBefore(subject: Locator, over: Locator) {
  const page = subject.page();
  const from = await center(subject);
  const toBox = (await over.boundingBox())!;
  const to = { x: toBox.x + toBox.width * 0.15, y: toBox.y + toBox.height / 2 };
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(from.x + 6, from.y + 2, { steps: 3 });
  await page.mouse.move(to.x, to.y, { steps: 25 });
  await page.mouse.up();
}

const finder = (page: Page) => page.locator('.products-finder.cat-editable');
const grid = (page: Page) => page.locator('#buttons-grid');
const toggle = (page: Page) => page.locator('[data-testid="designer-edit-toggle"]');
const catTab = (page: Page, name: string) => page.getByRole('tab', { name, exact: true });
const catCellOf = (page: Page, name: string) =>
  page.locator('.cat-tab-cell', { has: page.getByRole('tab', { name, exact: true }) });
// Every real category tab's label, in strip (DOM) order -- hidden or not.
const allTabOrder = (page: Page) => page.locator('.products-finder .tab-bar .tab[data-cat-tab]').allTextContents();
// The reorderable cells' category ids in strip order: what Done posts.
const cellIDOrder = (page: Page) =>
  page.locator('.products-finder .tab-bar .cat-tab-cell').evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.catId));
const fixtureTabOrder = (page: Page, run: string) =>
  page
    .locator(`.products-finder .tab-bar .tab[data-cat-tab]`)
    .evaluateAll((els, run) => els.map((el) => el.textContent!.trim()).filter((t) => t.includes(run)), run);

async function inViewport(el: Locator, w: number, h: number) {
  const b = (await el.boundingBox())!;
  expect(b.x).toBeGreaterThanOrEqual(0);
  expect(b.y).toBeGreaterThanOrEqual(0);
  expect(b.x + b.width).toBeLessThanOrEqual(w + 0.5);
  expect(b.y + b.height).toBeLessThanOrEqual(h + 0.5);
}

test.describe('Designer live sale-screen replica (ut-docs#2174)', () => {
  // One long, ordered journey (seed -> edit -> persist -> reload -> clean
  // up) against a per-worker till that is also booting for other specs;
  // the default budget is sized for a single interaction, not this.
  test.setTimeout(180_000);

  test('replica markup, edit-mode toggle, category rename/recolour/add/remove, tab reorder by keyboard and drag', async ({ page }) => {
    // Two refusals are provoked on purpose below (a blank name, a
    // deactivate blocked by remaining items) -- both are deliberate 400s
    // whose translated message the popover shows; Chromium logs every 4xx
    // fetch as a console error, so only that line is exempt.
    const assertClean = watchConsole(page, /status of 400/);
    await page.setViewportSize({ width: 1024, height: 600 });
    const f = fixture('1');
    const BUTTONS = [f.A1, f.A2, f.B1];
    await seedItems(page, BUTTONS);
    const categoryIDs: string[] = [];

    const apiCalls: Request[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/')) apiCalls.push(r);
    });

    try {
      await page.goto('/designer');

      // (0) The replica IS the sale screen's panel: the same strip/tabs/
      // tile markup, hosted in the Designer's editable section, refetching
      // itself from the Designer route. The fixture's category tabs and
      // tiles are there; the retired flat list is not.
      await expect(finder(page)).toBeVisible();
      await expect(page.locator('.products[hx-get="/ui/designer/buttons"]')).toHaveCount(1);
      await expect(page.locator('#buttons-grid-admin')).toHaveCount(0);
      await expect(catTab(page, f.catA)).toBeVisible();
      await expect(catTab(page, f.catB)).toBeVisible();
      await catTab(page, f.catA).click();
      const tileA1 = page.locator(`.products-tab-panel .btn-tile[data-code="${f.A1.barcode}"]`);
      await expect(tileA1).toBeVisible();
      await expect(tileA1.locator('xpath=..')).toHaveClass(/tile-cell/);
      // The add-a-button search still has its home on the page, below.
      await expect(page.locator('#search')).toBeVisible();
      // At rest: no edit affordances, no jiggle, the toggle unpressed.
      await expect(toggle(page)).toBeVisible();
      await expect(toggle(page)).toHaveAttribute('aria-pressed', 'false');
      await expect(page.locator('[data-testid="cat-tab-edit"]').first()).toBeHidden();
      await expect(page.locator('[data-testid="designer-cat-add"]')).toBeHidden();
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      // The whole replica fits the kiosk viewport with the search reachable
      // under it (bounded height + internal scroll, never a page overflow).
      await inViewport(finder(page), 1024, 600);
      const cellA = catCellOf(page, f.catA);
      const cellB = catCellOf(page, f.catB);
      categoryIDs.push((await cellA.getAttribute('data-cat-id'))!, (await cellB.getAttribute('data-cat-id'))!);

      // (1) The strip's pencil toggles the mode: tiles jiggle, the strip
      // gains its pencils and + tab, the tablist bar shows -- and NOTHING
      // was requested (entering is a pure class toggle, as on the sale
      // screen).
      apiCalls.length = 0;
      await toggle(page).click();
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(finder(page)).toHaveClass(/cat-edit-mode/);
      await expect(toggle(page)).toHaveAttribute('aria-pressed', 'true');
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
      const pencilA = cellA.locator('[data-testid="cat-tab-edit"]');
      await expect(pencilA).toBeVisible();
      await expect(page.locator('[data-testid="designer-cat-add"]')).toBeVisible();
      const pb = (await pencilA.boundingBox())!;
      expect(pb.width, 'pencil honours the 44px touch floor').toBeGreaterThanOrEqual(44);
      expect(pb.height).toBeGreaterThanOrEqual(44);
      expect(apiCalls, 'entering the mode must make no request').toHaveLength(0);
      // A category tab wobbles like a tile (animation wired; the fixture
      // runs reduced-motion so its duration is zeroed, same as the tiles).
      await expect
        .poll(() => catTab(page, f.catA).evaluate((el) => getComputedStyle(el).animationName))
        .toBe('ut-jiggle');

      // (2) Rename + recolour through the pencil's popover. The popover
      // sits inside the viewport and leaves the tile grid on screen.
      await pencilA.click();
      const popA = page.locator(`#cat-edit-${categoryIDs[0]}`);
      await expect(popA).toBeVisible();
      await inViewport(popA, 1024, 600);
      const gridBox = (await grid(page).boundingBox())!;
      expect(gridBox.y, 'the popover must not push the tile grid off screen').toBeLessThan(600);
      await expect(tileA1).toBeVisible();
      const nameA = popA.locator('[data-testid="cat-popover-name"]');
      await expect(nameA).toBeFocused();
      await expect(nameA).toHaveValue(f.catA);
      const renamed = `${f.catA} R`;
      await nameA.fill(renamed);
      // Pick the second palette swatch (the item picker's own tiles).
      const swatch = popA.locator('.item-color-tile[data-color]:not([data-color=""])').nth(1);
      const hex = (await swatch.getAttribute('data-color'))!;
      await swatch.click();
      await expect(swatch).toHaveAttribute('aria-pressed', 'true');
      await expect(popA.locator('[data-testid="cat-popover-color"]')).toHaveValue(hex);
      const saveResp = page.waitForResponse(
        (r) => r.url().endsWith(`/api/designer/categories/${categoryIDs[0]}`) && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await popA.locator('[data-testid="cat-popover-save"]').click();
      expect((await saveResp).status()).toBe(204);
      // The replica re-rendered in place: new name on the tab, its colour
      // carried as --cat-color, still in edit mode (no navigation, no
      // reload), and the same category still selected.
      await expect(catTab(page, renamed)).toBeVisible();
      await expect(catTab(page, f.catA)).toHaveCount(0);
      expect(await catTab(page, renamed).getAttribute('style')).toContain(hex);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(finder(page)).toHaveClass(/cat-edit-mode/);
      await expect(page).toHaveURL(/\/designer$/);
      await expect(catTab(page, renamed)).toHaveAttribute('aria-selected', 'true');

      // (2a) A blank name is refused IN the popover with /categories' own
      // copy; the popover stays open, nothing re-rendered.
      await catCellOf(page, renamed).locator('[data-testid="cat-tab-edit"]').click();
      const popA2 = page.locator(`#cat-edit-${categoryIDs[0]}`);
      await expect(popA2).toBeVisible();
      await popA2.locator('[data-testid="cat-popover-name"]').fill('   ');
      await popA2.locator('[data-testid="cat-popover-save"]').click();
      await expect(popA2.locator('.cat-popover-msg')).toContainText('Name is required');
      await expect(popA2).toBeVisible();

      // (2b) Remove on a category that still has items is refused with the
      // count -- ErrCategoryHasItems surfaced through the same translated
      // key /categories uses -- after htmx's own hx-confirm.
      let confirmText = '';
      page.once('dialog', (d) => { confirmText = d.message(); d.accept(); });
      const blocked = page.waitForResponse(
        (r) => r.url().endsWith(`/api/designer/categories/${categoryIDs[0]}/active`) && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await popA2.locator('[data-testid="cat-popover-remove"]').click();
      expect((await blocked).status()).toBe(400);
      expect(confirmText).toContain('Deactivate this category?');
      await expect(popA2.locator('.cat-popover-msg')).toContainText('2 active item(s)');
      await expect(catTab(page, renamed)).toBeVisible();
      // Escape closes the popover first (a second Escape would exit the
      // mode); focus returns to the pencil that opened it.
      await page.keyboard.press('Escape');
      await expect(popA2).toBeHidden();
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(catCellOf(page, renamed).locator('[data-testid="cat-tab-edit"]')).toBeFocused();

      // (3) + adds a category: it appears as a tab straight away (the
      // Designer keeps empty categories), with the empty-panel note.
      await page.locator('[data-testid="designer-cat-add"]').click();
      const popAdd = page.locator('#cat-add');
      await expect(popAdd).toBeVisible();
      await inViewport(popAdd, 1024, 600);
      const newCat = `Replica2174 New ${f.run}`;
      await popAdd.locator('[data-testid="cat-popover-name"]').fill(newCat);
      const created = page.waitForResponse(
        (r) => r.url().endsWith('/api/designer/categories') && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await popAdd.locator('[data-testid="cat-popover-save"]').click();
      expect((await created).status()).toBe(204);
      await expect(catTab(page, newCat)).toBeVisible();
      const newID = (await catCellOf(page, newCat).getAttribute('data-cat-id'))!;
      categoryIDs.push(newID);
      await catTab(page, newCat).click();
      await expect(page.locator('[data-testid="designer-category-empty"]:visible')).toHaveCount(1);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);

      // (4) Keyboard reorder of a category tab: ArrowLeft on the focused
      // new tab moves it one place earlier IN THE WHOLE STRIP (the demo
      // catalog's own categories sit between this fixture's tabs, so the
      // expectation is computed from the full order, not assumed) -- the
      // tablist's own roving focus stands down, so focus stays on the
      // moved tab. Nothing is posted until Done, then exactly ONE reorder
      // POST whose ids are the strip's cells in their new DOM order.
      apiCalls.length = 0;
      await expect.poll(() => fixtureTabOrder(page, f.run)).toEqual([renamed, f.catB, newCat]);
      const before4 = await allTabOrder(page);
      const at4 = before4.indexOf(newCat);
      expect(at4).toBeGreaterThan(0);
      const expected4 = before4.filter((t) => t !== newCat);
      expected4.splice(at4 - 1, 0, newCat);
      await catTab(page, newCat).focus();
      await page.keyboard.press('ArrowLeft');
      await expect.poll(() => allTabOrder(page)).toEqual(expected4);
      await expect(catTab(page, newCat)).toBeFocused();
      expect(apiCalls.filter((r) => r.url().includes('/reorder')), 'moving must make no request').toHaveLength(0);
      const idsBeforeDone = await cellIDOrder(page);
      const reorder = page.waitForResponse(
        (r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await page.locator('[data-testid="jiggle-done"]').click();
      expect((await reorder).status()).toBe(204);
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(finder(page)).not.toHaveClass(/cat-edit-mode/);
      await expect(toggle(page)).toHaveAttribute('aria-pressed', 'false');
      await page.waitForTimeout(200);
      const reorders = apiCalls.filter((r) => r.url().endsWith('/api/designer/categories/reorder'));
      expect(reorders, 'Done must persist with exactly one POST').toHaveLength(1);
      // The body is multipart FormData (repeated "ids" fields, the exact
      // shape /api/categories/reorder takes): parse them out of the raw body.
      const rawBody = reorders[0].postData() || '';
      const ids = Array.from(rawBody.matchAll(/name="ids"\r?\n\r?\n([^\r\n]+)/g)).map((m) => m[1]);
      expect(ids).toEqual(idsBeforeDone);
      // Survives a reload: the strip renders in the persisted order, and
      // the sale screen itself (which hides the empty category) sees the
      // rename.
      await page.reload();
      await expect.poll(() => allTabOrder(page)).toEqual(expected4);
      // CSS locators, not getByRole: the sale screen's narrower strip (the
      // basket takes half the width) hides tabs that don't fit behind its
      // '...' sheet (ut-docs#2307), and a `hidden` tab is not in the
      // accessibility tree -- what matters here is that the SERVER
      // renders the rename and prunes the empty category.
      await page.goto('/');
      await expect(page.locator('.tab[data-cat-tab]', { hasText: renamed })).toHaveCount(1);
      await expect(page.locator('.tab[data-cat-tab]', { hasText: newCat })).toHaveCount(0);
      await page.goto('/designer');

      // (5) Pointer drag of a category tab, entered via a hold on a TILE
      // (the sale screen's own entry point still works here): drag the new
      // tab back to the START half of CatB, so it lands just before CatB
      // (an earlier sibling reorders when the pointer crosses its midpoint
      // toward the inline-start, mirroring the tile drag); a tap outside
      // the replica exits and persists.
      await catTab(page, renamed).click();
      const tileHold = page.locator(`.products-tab-panel .btn-tile[data-code="${f.A1.barcode}"]`);
      const c = await center(tileHold);
      await page.mouse.move(c.x, c.y);
      await page.mouse.down();
      await page.waitForTimeout(700);
      await page.mouse.up();
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(finder(page)).toHaveClass(/cat-edit-mode/);
      const before5 = await allTabOrder(page);
      const expected5 = before5.filter((t) => t !== newCat);
      expected5.splice(expected5.indexOf(f.catB), 0, newCat);
      await dragBefore(catTab(page, newCat), catTab(page, f.catB));
      await expect.poll(() => allTabOrder(page)).toEqual(expected5);
      const reorder2 = page.waitForResponse(
        (r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await page.locator('h1').click(); // the page heading: outside the replica
      expect((await reorder2).status()).toBe(204);
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await page.reload();
      await expect.poll(() => allTabOrder(page)).toEqual(expected5);

      // (6) Removing the (empty) new category succeeds and the tab is gone
      // from the replica immediately.
      await toggle(page).click();
      await catCellOf(page, newCat).locator('[data-testid="cat-tab-edit"]').click();
      const popNew = page.locator(`#cat-edit-${newID}`);
      await expect(popNew).toBeVisible();
      page.once('dialog', (d) => d.accept());
      const removed = page.waitForResponse(
        (r) => r.url().endsWith(`/api/designer/categories/${newID}/active`) && r.request().method() === 'POST',
        { timeout: 15_000 },
      );
      await popNew.locator('[data-testid="cat-popover-remove"]').click();
      expect((await removed).status()).toBe(204);
      await expect(catTab(page, newCat)).toHaveCount(0);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      // Escape with no popover open exits the mode.
      await page.keyboard.press('Escape');
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="cat-tab-edit"]').first()).toBeHidden();

      // (7) A plain tap on a tile is never a sale here (no basket on this
      // page) -- it enters the mode instead of firing a request.
      apiCalls.length = 0;
      await catTab(page, renamed).click();
      await tileHold.click();
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(200);
      expect(apiCalls.filter((r) => r.url().includes('/api/pos/'))).toHaveLength(0);
      await page.keyboard.press('Escape');

      assertClean();
    } finally {
      await cleanup(page, BUTTONS, categoryIDs);
    }
  });

  test('the add-a-button search still adds a tile straight into the replica', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    const f = fixture('2');
    await seedItems(page, [f.A1], false); // catalog-only, no button yet
    const categoryIDs: string[] = [];
    try {
      await page.goto('/designer');
      await expect(finder(page)).toBeVisible();
      // Not a quick button yet: no tile in a category panel (the All grid
      // may list it -- that is every catalog item -- so scope to panels).
      await expect(page.locator(`.products-tab-panel .btn-tile[data-code="${f.A1.barcode}"]`)).toHaveCount(0);
      await page.locator('#search').pressSequentially(f.A1.name.slice(0, 16), { delay: 15 });
      const result = page.locator('#designer-search-results .result', { hasText: f.A1.name });
      await expect(result).toBeVisible({ timeout: 5000 });
      const added = page.waitForResponse((r) => r.url().includes('/api/buttons/add'), { timeout: 15_000 });
      await result.click();
      expect((await added).status()).toBe(204);
      // buttons-changed re-rendered the replica: the category now exists
      // as a tab (its first button just landed) and holds the tile.
      await expect(catTab(page, f.catA)).toBeVisible();
      categoryIDs.push((await catCellOf(page, f.catA).getAttribute('data-cat-id'))!);
      await catTab(page, f.catA).click();
      await expect(page.locator(`.products-tab-panel .btn-tile[data-code="${f.A1.barcode}"]`)).toBeVisible();
      await expect(page.locator('#designer-search-results')).toBeHidden();
      assertClean();
    } finally {
      await cleanup(page, [f.A1], categoryIDs);
    }
  });
});
