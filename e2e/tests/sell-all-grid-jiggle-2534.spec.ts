import { test, expect } from './fixtures';
import type { Page, Locator, Request } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#2534: the all_filter_chips browsing mode's All grid
// (#buttons-grid-all, under a row of category chips) now enters the jiggle
// edit mode on a long press, like every other mode. Under a category chip
// the grid shows that category in the global quick-button order and a drag
// saves THAT category's order (POST /api/buttons/reorder scope=subset, the
// server re-deals the codes into the slots they already hold); with no
// filter (plain All, alphabetical) the mode is edit-only -- the pencil
// badge, nothing moves. A chip tap while editing keeps the mode and saves a
// pending move first. The default project's till runs with auth off, so the
// session is granted (data-edit-allowed); the cashier side is
// sell-all-grid-jiggle-locked-cashier-2534.spec.ts on the auth project.
//
// HONESTY NOTE: same as sell-tile-jiggle-mode-2339.spec.ts -- gestures are
// Playwright's synthetic mouse pointer in Chromium, not a finger on the
// pilot till's WebKitGTK kiosk.

const RUN = Date.now().toString(36).toUpperCase();
type Item = { name: string; sku: string; category: string };
function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  const catA = `AllJig2534 A ${run}`;
  const catB = `AllJig2534 B ${run}`;
  const it = (n: string, cat: string): Item => ({ name: `AllJig2534 ${n} ${run}`, sku: `AJ2534${n}${run}`, category: cat });
  return {
    catA, catB,
    A: [it('Alpha', catA), it('Bravo', catA), it('Charlie', catA)],
    B: [it('Delta', catB), it('Echo', catB)],
  };
}

async function seed(page: Page, items: Item[], cats: string[]): Promise<Record<string, string>> {
  const csv = 'Name,SKU,Barcode,Price,Category,In stock\n' + items.map((i) => `${i.name},${i.sku},,1.00,${i.category},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-2534-${RUN}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/categories');
  const ids: Record<string, string> = {};
  for (const name of cats) {
    const row = page.locator(`.category-row[data-field-name="${name}"]`);
    await expect(row, `category row for ${name}`).toHaveCount(1);
    ids[name] = (await row.getAttribute('data-id'))!;
  }
  return ids;
}

async function cleanup(page: Page, items: Item[]) {
  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

async function center(el: Locator) {
  const box = (await el.boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}
async function longPress(tile: Locator) {
  await tile.scrollIntoViewIfNeeded();
  const page = tile.page();
  const c = await center(tile);
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}
// Drag `tile` to just before `over` (past its leading midpoint).
async function dragBefore(tile: Locator, over: Locator) {
  const page = tile.page();
  const from = await center(tile);
  const box = (await over.boundingBox())!;
  const to = { x: box.x + box.width * 0.2, y: box.y + box.height / 2 };
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(from.x - 6, from.y + 2, { steps: 3 });
  await page.mouse.move(to.x, to.y, { steps: 25 });
  await page.mouse.up();
}

const allGrid = (page: Page) => page.locator('#buttons-grid-all');
const tileOf = (page: Page, it: Item) => allGrid(page).locator(`.btn-tile[data-name="${it.name}"]`);
const cellOf = (page: Page, it: Item) => tileOf(page, it).locator('xpath=..');
const touchAction = (tile: Locator) => tile.evaluate((el) => getComputedStyle(el).touchAction);
const namesIn = (page: Page, prefix: string) =>
  allGrid(page).locator(`.btn-tile[data-name^="${prefix}"]`).evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.name));

async function pickChip(page: Page, catId: string) {
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes(`category=${catId}`)),
    page.locator(`#browsing-category-chips .chip[data-cat-id="${catId}"]`).click(),
  ]);
}

test.describe('All grid jiggle edit mode (ut-docs#2534)', () => {
  test.beforeEach(async ({ page }) => {
    await setBrowsingMode(page, 'all_filter_chips');
  });
  test.afterEach(async ({ page }) => {
    // Every worker till boots in strip_overflow (worker-till.ts).
    await setBrowsingMode(page, 'strip_overflow');
  });

  test('under a category chip: long press arms with the pencil only, a drag saves that category order and leaves the other one alone', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('1');
    const all = [...f.A, ...f.B];
    const ids = await seed(page, all, [f.catA, f.catB]);
    const prefix = `AllJig2534 `;
    const reorders: Request[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/buttons/reorder')) reorders.push(r); });
    try {
      await page.goto('/');
      await expect(allGrid(page)).toHaveAttribute('data-edit-allowed', '');
      await pickChip(page, ids[f.catB]);
      await expect.poll(() => namesIn(page, prefix)).toEqual(f.B.map((i) => i.name));
      await pickChip(page, ids[f.catA]);
      await expect.poll(() => namesIn(page, prefix)).toEqual(f.A.map((i) => i.name));

      await longPress(tileOf(page, f.A[0]));
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
      // This grid is items, not quick buttons: the bar says what it does here.
      await expect(page.locator('.jiggle-bar-title')).toHaveText('Editing items — choose a category to drag its items into order; the pencil edits an item');
      await expect(page.locator('.basket .line-name')).toHaveCount(0); // the hold's trailing click never sold
      for (const it of f.A) {
        await expect(cellOf(page, it).locator('[data-testid="tile-badge-edit"]')).toBeVisible();
        await expect(cellOf(page, it).locator('[data-testid="tile-badge-remove"], [data-testid="tile-badge-hide"], [data-testid="tile-badge-unhide"]')).toHaveCount(0);
      }
      expect(reorders, 'entering the mode makes no reorder call').toHaveLength(0);
      // Movable under a chip: the drag owns the finger (app.js's
      // all-grid-movable class, not a CSS :has()).
      await expect(allGrid(page)).toHaveClass(/all-grid-movable/);
      expect(await touchAction(tileOf(page, f.A[0]))).toBe('none');

      // Charlie to the front of Alpha: Charlie, Alpha, Bravo.
      await dragBefore(tileOf(page, f.A[2]), tileOf(page, f.A[0]));
      const wanted = [f.A[2], f.A[0], f.A[1]].map((i) => i.name);
      await expect.poll(() => namesIn(page, prefix)).toEqual(wanted);
      expect(reorders, 'nothing is saved mid-drag').toHaveLength(0);

      const saved = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST');
      await page.locator('[data-testid="jiggle-done"]').click();
      const res = await saved;
      expect(res.status()).toBe(204);
      const body = new URLSearchParams(res.request().postData() || '');
      expect(body.get('scope')).toBe('subset');
      const codes = body.getAll('codes');
      expect(codes).toHaveLength(3); // this category's tiles only, never the global list
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);
      expect(reorders).toHaveLength(1);

      // Persisted: after a reload the chip shows the new order...
      await page.reload();
      await pickChip(page, ids[f.catA]);
      await expect.poll(() => namesIn(page, prefix)).toEqual(wanted);
      // ...the other category's order is untouched...
      await pickChip(page, ids[f.catB]);
      await expect.poll(() => namesIn(page, prefix)).toEqual(f.B.map((i) => i.name));
      // ...and it is the order the category modes use (the category_tabs
      // popup lists quick buttons in that same order).
      const popup = await (await page.request.get(`/ui/buttons/category?id=${ids[f.catA]}`)).text();
      const at = wanted.map((n) => popup.indexOf(`data-name="${n}"`));
      expect(at.every((i) => i >= 0), 'popup lists the category items').toBe(true);
      expect([...at].sort((a, b) => a - b)).toEqual(at);

      assertClean();
    } finally {
      await cleanup(page, all);
    }
  });

  // Review fix: the save bumps sell_screen_version, and the open screen's
  // own live-refresh watcher (web/public/sell-screen-watch.js, a real 5 s
  // poll -- no test hook shortens it) used to see that as a change made
  // elsewhere and re-render the grid, dropping the chip back to plain All.
  // The reorder answers with X-UT-Sell-Version and the jiggle save records
  // it, so the watcher treats this till's own save as already rendered.
  test('after Done under a chip, the chip and its new order survive the live-refresh watcher', async ({ page }) => {
    test.setTimeout(60_000);
    const assertClean = watchConsole(page);
    const f = fixture('4');
    const all = [...f.A, ...f.B];
    const ids = await seed(page, all, [f.catA, f.catB]);
    const prefix = `AllJig2534 `;
    let gridFetches = 0;
    let polls = 0;
    page.on('request', (r) => {
      const path = new URL(r.url()).pathname;
      if (r.method() === 'GET' && path === '/ui/buttons') gridFetches++;
    });
    page.on('response', (r) => {
      if (new URL(r.url()).pathname === '/ui/buttons/version') polls++;
    });
    try {
      await page.goto('/');
      await pickChip(page, ids[f.catA]);
      const chip = page.locator(`#browsing-category-chips .chip[data-cat-id="${ids[f.catA]}"]`);
      await expect(chip).toHaveAttribute('aria-pressed', 'true');

      await longPress(tileOf(page, f.A[0]));
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await dragBefore(tileOf(page, f.A[2]), tileOf(page, f.A[0]));
      const wanted = [f.A[2], f.A[0], f.A[1]].map((i) => i.name);
      await expect.poll(() => namesIn(page, prefix)).toEqual(wanted);

      const saved = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST');
      await page.locator('[data-testid="jiggle-done"]').click();
      const res = await saved;
      expect(res.status()).toBe(204);
      expect(res.headers()['x-ut-sell-version'], 'the save reports the version it moved the screen to').toMatch(/^\d+$/);
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);

      // Two full watcher polls after the save -- each one would have fired
      // the refresh -- then time for a refresh it fired to land.
      const fetchesAtSave = gridFetches;
      const pollsAtSave = polls;
      await expect.poll(() => polls - pollsAtSave, { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
      await page.waitForTimeout(1500);

      await expect(chip).toHaveAttribute('aria-pressed', 'true');
      await expect(page.locator('#browsing-category-chips [data-cat-all]')).toHaveAttribute('aria-pressed', 'false');
      expect(await namesIn(page, prefix)).toEqual(wanted);
      expect(gridFetches - fetchesAtSave, 'the till\'s own save never re-renders its grid').toBe(0);
      assertClean();
    } finally {
      await cleanup(page, all);
    }
  });

  test('plain All (no chip): long press arms edit-only -- pencil, no drag, no save', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('2');
    const all = [...f.A, ...f.B];
    await seed(page, all, [f.catA, f.catB]);
    const reorders: Request[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/buttons/reorder')) reorders.push(r); });
    try {
      await page.goto('/');
      await expect(page.locator('#browsing-category-chips [data-cat-all]')).toHaveAttribute('aria-pressed', 'true');
      await expect(allGrid(page).locator('[data-all-filter]')).toHaveCount(0);
      const alpha = tileOf(page, f.A[0]);
      const bravo = tileOf(page, f.A[1]);
      await expect(alpha).toBeVisible();

      await longPress(alpha);
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(cellOf(page, f.A[0]).locator('[data-testid="tile-badge-edit"]')).toBeVisible();
      await expect(cellOf(page, f.A[0]).locator('[data-testid="tile-badge-remove"], [data-testid="tile-badge-hide"]')).toHaveCount(0);
      // Edit-only: the finger still pans the grid.
      await expect(allGrid(page)).not.toHaveClass(/all-grid-movable/);
      expect(await touchAction(alpha)).toBe('manipulation');

      const before = await namesIn(page, 'AllJig2534 ');
      await bravo.scrollIntoViewIfNeeded();
      await dragBefore(bravo, alpha);
      await alpha.focus();
      await page.keyboard.press('ArrowRight');
      expect(await namesIn(page, 'AllJig2534 '), 'nothing moves in plain All').toEqual(before);

      await page.locator('[data-testid="jiggle-done"]').click();
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(300);
      expect(reorders, 'plain All never saves an order').toHaveLength(0);
      assertClean();
    } finally {
      await cleanup(page, all);
    }
  });

  test('a chip tap mid-edit keeps edit mode and saves the pending move first', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('3');
    const all = [...f.A, ...f.B];
    const ids = await seed(page, all, [f.catA, f.catB]);
    const prefix = `AllJig2534 `;
    const order: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/buttons/reorder')) order.push('reorder');
      if (r.url().includes('/ui/buttons/all/more')) order.push('chip');
    });
    try {
      await page.goto('/');
      await pickChip(page, ids[f.catA]);
      await longPress(tileOf(page, f.A[0]));
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);

      // Keyboard move: Alpha one place later -> Bravo, Alpha, Charlie.
      await tileOf(page, f.A[0]).focus();
      await page.keyboard.press('ArrowRight');
      const wanted = [f.A[1], f.A[0], f.A[2]].map((i) => i.name);
      await expect.poll(() => namesIn(page, prefix)).toEqual(wanted);

      order.length = 0;
      const saved = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder'));
      await pickChip(page, ids[f.catB]);
      expect((await saved).status()).toBe(204);
      expect(order, 'the move is saved before the chip swaps the tiles').toEqual(['reorder', 'chip']);

      // Still editing, and the new category's tiles got their pencil.
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
      await expect.poll(() => namesIn(page, prefix)).toEqual(f.B.map((i) => i.name));
      await expect(cellOf(page, f.B[0]).locator('[data-testid="tile-badge-edit"]')).toBeVisible();
      await expect(allGrid(page)).toHaveClass(/all-grid-movable/);
      expect(await touchAction(tileOf(page, f.B[0]))).toBe('none');

      // Nothing pending any more: Done saves nothing further.
      order.length = 0;
      await page.locator('[data-testid="jiggle-done"]').click();
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(300);
      expect(order).toEqual([]);

      await page.reload();
      await pickChip(page, ids[f.catA]);
      await expect.poll(() => namesIn(page, prefix)).toEqual(wanted);
      assertClean();
    } finally {
      await cleanup(page, all);
    }
  });
});
