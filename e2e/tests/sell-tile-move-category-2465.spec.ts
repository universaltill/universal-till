import { test, expect } from './fixtures';
import type { Page, Locator, Request } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#2465: in the sell screen's jiggle edit mode (and the Designer's
// live replica of it, same #buttons-grid and app.js code), a quick button
// can be moved to ANOTHER category: drag it onto that category's tab in the
// strip (the tab lights up as a drop target), or -- the keyboard / screen
// reader / overflow-tab path -- use the tile's Move to category badge and
// pick a category in the dialog. Either one changes the ITEM's category
// (POST /api/buttons/recategorize), and the change survives a reload.
// The cashier half (no badge, the route answers with the PIN prompt) is in
// sale-only-cashier-3079.spec.ts, which runs on the auth project.
//
// HONESTY NOTE: gestures are Playwright's synthetic mouse (pointerType
// "mouse") in desktop Chromium, as in sell-tile-jiggle-mode-2339.spec.ts --
// not a finger on the pilot till's WebKitGTK.

const RUN = Date.now().toString(36).toUpperCase();

function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  // Short names: the strip shares the row with the basket, and a category
  // whose tab doesn't fit goes behind "..." (not a drop target).
  const short = `${RUN.slice(-3)}${tag}`;
  const cats = [`Mv${short}A`, `Mv${short}B`];
  const items = [
    { name: `MV2465 A1 ${run}`, sku: `MV2465A1${run}`, barcode: `MV2465-A1-${run}`, category: cats[0] },
    { name: `MV2465 A2 ${run}`, sku: `MV2465A2${run}`, barcode: `MV2465-A2-${run}`, category: cats[0] },
    { name: `MV2465 B1 ${run}`, sku: `MV2465B1${run}`, barcode: `MV2465-B1-${run}`, category: cats[1] },
  ];
  return { run, cats, items };
}
type Fixture = ReturnType<typeof fixture>;

async function seed(page: Page, f: Fixture) {
  await setBrowsingMode(page, 'strip_overflow');
  const csv = 'Name,SKU,Barcode,Price,Category,In stock\n' +
    f.items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: 'import-2465.csv', mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  for (const it of f.items) {
    const id = (await page.locator(`.catalog-row[data-name="${it.name}"]`).first().getAttribute('data-id'))!;
    const resp = await page.request.post('/api/buttons/add', { form: { itemId: id, label: it.name, code: it.barcode } });
    expect(resp.ok(), `add quick button for ${it.name}`).toBe(true);
  }
}

async function cleanup(page: Page, f: Fixture) {
  for (const it of f.items) await page.request.post('/api/buttons/remove', { form: { code: it.barcode } });
  await page.goto('/catalog');
  for (const it of f.items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

async function center(el: Locator) {
  const b = (await el.boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
}

async function longPress(tile: Locator) {
  const page = tile.page();
  const c = await center(tile);
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

const tab = (page: Page, name: string) => page.getByRole('tab', { name, exact: true });
// A tile inside the given category's own grid (data-grid-cat), never a
// search result or the other category's panel.
const tileIn = (page: Page, catTab: Locator, barcode: string) =>
  catTab.getAttribute('data-tab-id').then((id) => page.locator(`#buttons-grid .grid[data-grid-cat="${id}"] .btn-tile[data-code="${barcode}"]`));

test.describe('Move a quick button to another category (ut-docs#2465)', () => {
  test.beforeEach(() => { test.setTimeout(90_000); });
  test('drag a tile onto another category tab moves the item; survives reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('d');
    await seed(page, f);
    const [A1, A2] = f.items;
    try {
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto('/');
      const tabA = tab(page, f.cats[0]);
      const tabB = tab(page, f.cats[1]);
      await expect(tabA).toBeVisible();
      await expect(tabB).toBeVisible();
      await tabA.click();
      const a1 = await tileIn(page, tabA, A1.barcode);
      const a2 = await tileIn(page, tabA, A2.barcode);
      await expect(a1).toBeVisible();

      await longPress(a1);
      const grid = page.locator('#buttons-grid');
      await expect(grid).toHaveClass(/jiggle-mode/);
      // The Move to category badge shows in edit mode (this strip has tabs).
      await expect(a1.locator('xpath=..').getByTestId('tile-badge-move')).toBeVisible();

      const calls: Request[] = [];
      page.on('request', (r) => { if (r.url().includes('/api/buttons/')) calls.push(r); });

      // First a plain in-grid reorder (A2 before A1) -- still unsaved...
      const from2 = await center(a2);
      const to2 = (await a1.boundingBox())!;
      await page.mouse.move(from2.x, from2.y);
      await page.mouse.down();
      await page.mouse.move(from2.x - 6, from2.y + 2, { steps: 3 });
      await page.mouse.move(to2.x + to2.width * 0.2, to2.y + to2.height / 2, { steps: 20 });
      await page.mouse.up();
      expect(calls.length, 'an in-grid reorder is not saved per drag').toBe(0);

      // ...then drag A1 up onto B's tab. Over its OWN tab nothing lights up;
      // over B's, B becomes the drop target.
      const from = await center(a1);
      const aC = await center(tabA);
      const bC = await center(tabB);
      await page.mouse.move(from.x, from.y);
      await page.mouse.down();
      await page.mouse.move(from.x + 6, from.y + 2, { steps: 3 });
      await page.mouse.move(aC.x, aC.y, { steps: 20 });
      await expect(tabA).not.toHaveClass(/tile-drop-target/);
      await page.mouse.move(bC.x, bC.y, { steps: 10 });
      await expect(tabB).toHaveClass(/tile-drop-target/);
      const recat = page.waitForResponse((r) => r.url().includes('/api/buttons/recategorize'));
      await page.mouse.up();
      const res = await recat;
      expect(res.status()).toBe(204);
      await expect(tabB).not.toHaveClass(/tile-drop-target/);

      // The pending reorder was saved FIRST, then the move.
      const paths = calls.map((r) => new URL(r.url()).pathname);
      expect(paths.indexOf('/api/buttons/reorder'), 'pending reorder saved').toBeGreaterThanOrEqual(0);
      expect(paths.indexOf('/api/buttons/reorder')).toBeLessThan(paths.indexOf('/api/buttons/recategorize'));

      // Still editing, still on A; A1 left A's grid, A2 stayed.
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(tabA).toHaveAttribute('aria-selected', 'true');
      await expect(page.locator(`#buttons-grid .btn-tile[data-code="${A1.barcode}"]`)).toHaveCount(1);
      await expect(await tileIn(page, tabA, A1.barcode)).toHaveCount(0);
      await expect(await tileIn(page, tabA, A2.barcode)).toBeVisible();
      await page.getByTestId('jiggle-done').click();
      await tabB.click();
      await expect(await tileIn(page, tabB, A1.barcode)).toBeVisible();

      // Server truth: after a reload A1 is under B, A2 still under A.
      await page.reload();
      await expect(await tileIn(page, tab(page, f.cats[1]), A1.barcode)).toHaveCount(1);
      await expect(await tileIn(page, tab(page, f.cats[0]), A2.barcode)).toHaveCount(1);
      await expect(await tileIn(page, tab(page, f.cats[0]), A1.barcode)).toHaveCount(0);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('keyboard: the Move to category badge + dialog moves the item', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('k');
    await seed(page, f);
    const B1 = f.items[2];
    try {
      await page.setViewportSize({ width: 1024, height: 600 });
      await page.goto('/');
      const tabA = tab(page, f.cats[0]);
      const tabB = tab(page, f.cats[1]);
      const idA = (await tabA.getAttribute('data-tab-id'))!;
      const idB = (await tabB.getAttribute('data-tab-id'))!;
      await tabB.click();
      const b1 = await tileIn(page, tabB, B1.barcode);
      await expect(b1).toBeVisible();

      // Right-click (contextmenu) is the non-hold way in.
      await b1.click({ button: 'right' });
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      const badge = b1.locator('xpath=..').getByTestId('tile-badge-move');
      await expect(badge).toBeVisible();
      await expect(badge).toHaveAttribute('aria-label', new RegExp(B1.name));

      // Open with the keyboard; the tile's own category is not offered.
      await badge.focus();
      await page.keyboard.press('Enter');
      const dlg = page.locator('#tile-move-dialog');
      await expect(dlg).toBeVisible();
      await expect(dlg.locator('.tile-move-hint')).toContainText(B1.name);
      await expect(dlg.locator(`.tile-move-option[data-move-cat="${idB}"]`)).toBeHidden();
      await expect(dlg.locator(`.tile-move-option[data-move-cat="${idA}"]`)).toBeVisible();
      await expect(dlg.locator('.tile-move-option:focus')).toHaveCount(1);

      // Escape closes the dialog only -- still editing, focus back on the badge.
      await page.keyboard.press('Escape');
      await expect(dlg).toBeHidden();
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(badge).toBeFocused();

      // Cancel does the same.
      await page.keyboard.press('Enter');
      await expect(dlg).toBeVisible();
      await dlg.getByTestId('tile-move-cancel').click();
      await expect(dlg).toBeHidden();
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);

      // Choose A with the keyboard.
      await badge.focus();
      await page.keyboard.press('Enter');
      await expect(dlg).toBeVisible();
      await dlg.locator(`.tile-move-option[data-move-cat="${idA}"]`).focus();
      const recat = page.waitForResponse((r) => r.url().includes('/api/buttons/recategorize'));
      await page.keyboard.press('Enter');
      expect((await recat).status()).toBe(204);
      await expect(dlg).toBeHidden();
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await expect(page.getByTestId('jiggle-done')).toBeFocused();

      await page.reload();
      await expect(await tileIn(page, tab(page, f.cats[0]), B1.barcode)).toHaveCount(1);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('Designer replica: the badge dialog moves a tile there too', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('g');
    await seed(page, f);
    const A2 = f.items[1];
    try {
      await page.goto('/designer');
      const tabA = tab(page, f.cats[0]);
      const tabB = tab(page, f.cats[1]);
      const idB = (await tabB.getAttribute('data-tab-id'))!;
      await tabA.click();
      const a2 = await tileIn(page, tabA, A2.barcode);
      await expect(a2).toBeVisible();
      await a2.click({ button: 'right' });
      await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
      await a2.locator('xpath=..').getByTestId('tile-badge-move').click();
      const dlg = page.locator('#tile-move-dialog');
      await expect(dlg).toBeVisible();
      const recat = page.waitForResponse((r) => r.url().includes('/api/buttons/recategorize'));
      await dlg.locator(`.tile-move-option[data-move-cat="${idB}"]`).click();
      expect((await recat).status()).toBe(204);
      await expect(await tileIn(page, tab(page, f.cats[1]), A2.barcode)).toHaveCount(1);
      await page.reload();
      await expect(await tileIn(page, tab(page, f.cats[1]), A2.barcode)).toHaveCount(1);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('RTL (fa): badge sits at the inline-start bottom corner, dialog opens', async ({ page }) => {
    const f = fixture('r');
    await seed(page, f);
    try {
      await page.setViewportSize({ width: 1024, height: 600 });
      await page.goto('/?lang=fa');
      await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      const tabA = tab(page, f.cats[0]);
      await tabA.click();
      const a1 = await tileIn(page, tabA, f.items[0].barcode);
      await a1.click({ button: 'right' });
      const cell = a1.locator('xpath=..');
      const badge = cell.getByTestId('tile-badge-move');
      await expect(badge).toBeVisible();
      const t = (await a1.boundingBox())!;
      const b = (await badge.boundingBox())!;
      const hide = (await cell.getByTestId('tile-badge-hide').boundingBox())!;
      // Inline-start is the RIGHT edge in RTL; bottom corner; never on Hide.
      expect(b.x + b.width / 2).toBeGreaterThan(t.x + t.width / 2);
      expect(b.y + b.height / 2).toBeGreaterThan(t.y + t.height / 2);
      expect(Math.abs((b.x + b.width / 2) - (hide.x + hide.width / 2))).toBeGreaterThan(30);
      await badge.click();
      await expect(page.locator('#tile-move-dialog')).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(page.locator('#tile-move-dialog')).toBeHidden();
    } finally {
      await page.goto('/?lang=en');
      await cleanup(page, f);
    }
  });
});
