import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2698 (product owner: "after hiding a product ... it shouldn't
// disappear from the editing page, it should be gray and let me bring it
// back ... the delete will delete it from quick buttons and I can bring it
// back by search"):
//   * Hide (eye-off badge) keeps the tile in its spot, greyed, while the
//     grid is in jiggle edit mode, with an Unhide (eye) badge; at rest it is
//     out of sight; re-entering edit mode shows it greyed again.
//   * The trash badge removes the tile from the quick buttons -- gone in edit
//     mode too -- while the item stays in the catalog; the sale-screen search,
//     used while editing, offers "Add to quick buttons" to bring it back.
//   * The Designer shows hidden tiles greyed all the time.
//
// Same honesty note as sell-tile-jiggle-mode-2339.spec.ts: gestures are
// Playwright's synthetic mouse pointer in Chromium, not real touch on the
// pilot till's WebKitGTK kiosk.

const RUN = Date.now().toString(36).toUpperCase();
const CAT = `Grey2698 Cat ${RUN}`;
const A = { name: `Grey2698 Alpha ${RUN}`, sku: `GRY2698A${RUN}`, barcode: `GRY2698BC-A-${RUN}` };
const B = { name: `Grey2698 Bravo ${RUN}`, sku: `GRY2698B${RUN}`, barcode: `GRY2698BC-B-${RUN}` };
const C = { name: `Grey2698 Charlie ${RUN}`, sku: `GRY2698C${RUN}`, barcode: `GRY2698BC-C-${RUN}` };
const ALL = [A, B, C];

async function importItems(page: Page) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-2698-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(
      'Name,SKU,Barcode,Price,Category,In stock\n' +
        ALL.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${CAT},1`).join('\n'),
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
  return page.locator(`#buttons-grid .products-tab-panel .btn-tile[data-name="${name}"]`);
}
function cellOf(page: Page, name: string): Locator {
  return page.locator('#buttons-grid .products-tab-panel .tile-cell', { has: page.locator(`.btn-tile[data-name="${name}"]`) });
}
function grid(page: Page): Locator {
  return page.locator('#buttons-grid');
}
// The fixture category's tile names, in DOM order (hidden ones included).
async function namesInPanel(page: Page): Promise<string[]> {
  return page
    .locator(`#buttons-grid .products-tab-panel .btn-tile[data-name^="Grey2698"][data-name$="${RUN}"]`)
    .evaluateAll((els) => els.map((e) => e.getAttribute('data-name') || ''));
}

async function openCategory(page: Page) {
  await page.goto('/');
  await page.getByRole('tab', { name: CAT }).click();
  await expect(tile(page, A.name)).toBeVisible();
}

async function enterJiggle(page: Page, t: Locator) {
  await t.click({ button: 'right' });
  await expect(grid(page)).toHaveClass(/jiggle-mode/);
}

// WCAG relative-luminance contrast of an element's text colour against the
// nearest opaque background behind it.
async function textContrast(el: Locator): Promise<number> {
  return el.evaluate((node) => {
    const parse = (c: string) => (c.match(/[\d.]+/g) || []).map(Number);
    const lum = ([r, g, b]: number[]) => {
      const f = (v: number) => {
        const s = v / 255;
        return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
      };
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const fg = parse(getComputedStyle(node as Element).color);
    let bgEl: Element | null = node as Element;
    let bg = [255, 255, 255];
    while (bgEl) {
      const c = parse(getComputedStyle(bgEl).backgroundColor);
      if (c.length >= 3 && (c.length < 4 || c[3] > 0.9)) {
        bg = c;
        break;
      }
      bgEl = bgEl.parentElement;
    }
    const [l1, l2] = [lum(fg), lum(bg)].sort((x, y) => y - x);
    return (l1 + 0.05) / (l2 + 0.05);
  });
}

test.describe('hidden quick buttons stay greyed in edit mode; trash removes, search re-adds (ut-docs#2698)', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await importItems(page);
    await page.close();
  });

  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    for (const it of ALL) {
      const id = await itemId(page, it.name);
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
    }
    await page.close();
  });

  test('hide → greyed in place → Done → gone → edit again → greyed → unhide', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openCategory(page);
    const before = await namesInPanel(page);
    expect(before).toEqual([A.name, B.name, C.name]);

    await enterJiggle(page, tile(page, A.name));
    const cellB = cellOf(page, B.name);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/hide') && r.ok()),
      cellB.getByTestId('tile-badge-hide').click(),
    ]);
    // Still editing (the grid re-rendered and jiggle mode came back), and B
    // is still there, in the same spot, greyed, with an Unhide badge.
    await expect(grid(page)).toHaveClass(/jiggle-mode/);
    await expect(cellB).toHaveClass(/tile--hidden/);
    await expect(tile(page, B.name)).toBeVisible();
    await expect(tile(page, B.name)).toHaveAttribute('data-hidden', '');
    expect(await namesInPanel(page)).toEqual(before);
    const unhide = cellB.getByTestId('tile-badge-unhide');
    await expect(unhide).toBeVisible();
    await expect(cellB.getByTestId('tile-badge-hide')).toHaveCount(0);
    await expect(unhide).toHaveAttribute('aria-label', `Show on sell screen: ${B.name}`);
    const ub = (await unhide.boundingBox())!;
    expect(ub.width).toBeGreaterThanOrEqual(44);
    expect(ub.height).toBeGreaterThanOrEqual(44);
    // Greyed, but not by colour alone: the eye-off glyph is in the tile, and
    // the name keeps >= 4.5:1 contrast.
    await expect(tile(page, B.name).locator('.tile-hidden-mark')).toBeVisible();
    expect(await textContrast(tile(page, B.name).locator('.tile-name'))).toBeGreaterThanOrEqual(4.5);
    const shotDir = process.env.UT_SHOT_DIR;
    await page.screenshot({ path: shotDir ? `${shotDir}/2698-sale-edit-greyed.png` : test.info().outputPath('sale-edit-greyed.png') });
    if (shotDir) {
      // The kiosk floor and a phone width, for the UX gate's own look.
      const vp = page.viewportSize();
      for (const [w, h] of [[1024, 600], [360, 740]]) {
        await page.setViewportSize({ width: w, height: h });
        await tile(page, B.name).scrollIntoViewIfNeeded();
        await page.screenshot({ path: `${shotDir}/2698-sale-edit-greyed-${w}.png` });
      }
      if (vp) await page.setViewportSize(vp);
    }

    // Done: out of sight at rest; A and C unaffected.
    await page.getByTestId('jiggle-done').click();
    await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
    await expect(tile(page, B.name)).toBeHidden();
    await expect(tile(page, A.name)).toBeVisible();
    await expect(tile(page, C.name)).toBeVisible();

    // Re-entering edit mode shows B greyed again, same spot.
    await page.reload();
    await page.getByRole('tab', { name: CAT }).click();
    await expect(tile(page, B.name)).toBeHidden();
    await enterJiggle(page, tile(page, C.name));
    await expect(tile(page, B.name)).toBeVisible();
    await expect(cellB).toHaveClass(/tile--hidden/);
    expect(await namesInPanel(page)).toEqual(before);

    // Unhide from the badge: back to normal right away, same spot.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/unhide') && r.ok()),
      cellB.getByTestId('tile-badge-unhide').click(),
    ]);
    await expect(cellB).not.toHaveClass(/tile--hidden/);
    await expect(cellB.getByTestId('tile-badge-hide')).toBeVisible();
    await page.getByTestId('jiggle-done').click();
    await expect(tile(page, B.name)).toBeVisible();
    expect(await namesInPanel(page)).toEqual(before);
    assertClean();
  });

  test('trash → gone even in edit mode, item kept → search in edit mode → Add to quick buttons → back', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openCategory(page);
    await enterJiggle(page, tile(page, A.name));

    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/remove-from-grid') && r.ok()),
      cellOf(page, C.name).getByTestId('tile-badge-remove').click(),
    ]);
    await expect(grid(page)).toHaveClass(/jiggle-mode/);
    await expect(tile(page, C.name)).toHaveCount(0);

    // Search stays usable while editing -- opening it does not leave edit mode.
    await page.locator('.products-strip-search').click();
    await expect(grid(page)).toHaveClass(/jiggle-mode/);
    await page.locator('#products-search').fill(C.name);
    const result = page.locator('#search-results .btn-tile', { hasText: C.name });
    await expect(result).toBeVisible({ timeout: 5000 });
    // A tap on a result while editing never rings it up.
    const basketLines = page.locator('.basket .line-name');
    const linesBefore = await basketLines.count();
    await result.click();
    await page.waitForTimeout(300);
    await expect(basketLines).toHaveCount(linesBefore);

    const idC = await result.locator('xpath=..').getByRole('button', { name: `Add to quick buttons: ${C.name}` }).getAttribute('data-testid');
    expect(idC).toMatch(/^search-add-quick-/);
    const add = page.getByTestId(idC!);
    await expect(add).toBeVisible();
    const shotDir = process.env.UT_SHOT_DIR;
    await page.screenshot({ path: shotDir ? `${shotDir}/2698-sale-edit-search-add.png` : test.info().outputPath('sale-edit-search-add.png') });
    await Promise.all([page.waitForResponse((r) => r.url().includes('/api/buttons/add') && r.ok()), add.click()]);
    // The refreshed results no longer offer it: it is a quick button again.
    await expect(page.locator('#search-results .btn-tile', { hasText: C.name })).toBeVisible({ timeout: 5000 });
    await expect(page.getByTestId(idC!)).toHaveCount(0);

    // Back to the grid, still editing: the tile is back.
    await page.locator('.products-strip-back').click();
    await expect(grid(page)).toHaveClass(/jiggle-mode/);
    await expect(tile(page, C.name)).toBeVisible();
    await page.getByTestId('jiggle-done').click();
    await expect(tile(page, C.name)).toBeVisible();
    // The catalog item was never touched.
    expect(await itemId(page, C.name)).not.toBeNull();
    assertClean();
  });

  test('a category whose tiles are all hidden: no tab at rest, reachable while editing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const ids: string[] = [];
    for (const it of ALL) ids.push((await itemId(page, it.name))!);
    for (const id of ids) expect((await page.request.post('/api/buttons/hide', { form: { itemId: id } })).ok()).toBe(true);
    try {
      await page.goto('/');
      const catTab = page.locator('.products-finder .tab-bar .tab', { hasText: CAT });
      await expect(catTab).toHaveCount(1);
      await expect(catTab).toBeHidden();
      // Enter edit mode from any other visible tile; the category's tab
      // appears, and switching to it does not leave edit mode.
      await enterJiggle(page, page.locator('#buttons-grid .products-tab-panel .btn-tile[data-code]:visible').first());
      await expect(catTab).toBeVisible();
      await catTab.click();
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      for (const it of ALL) await expect(tile(page, it.name)).toBeVisible();
      await expect(cellOf(page, A.name)).toHaveClass(/tile--hidden/);
      await expect(page.locator('#buttons-grid .products-tab-panel:visible [data-testid="category-empty-state"]')).toBeHidden();
      await page.getByTestId('jiggle-done').click();
      await expect(catTab).toBeHidden();
    } finally {
      for (const id of ids) await page.request.post('/api/buttons/unhide', { form: { itemId: id } });
    }
    assertClean();
  });

  // Review F2: hiding the last visible tile of the CURRENT category while
  // editing makes its tab hidden-only; on Done that tab disappears, so the
  // strip must move to a visible tab -- never leave the vanished one
  // selected over an empty "No quick buttons" panel.
  test('hide the last visible tile of the current category in edit mode → Done → a visible tab is selected', async ({ page }) => {
    const assertClean = watchConsole(page);
    const idA = (await itemId(page, A.name))!;
    const idB = (await itemId(page, B.name))!;
    const idC = (await itemId(page, C.name))!;
    for (const id of [idA, idB]) expect((await page.request.post('/api/buttons/hide', { form: { itemId: id } })).ok()).toBe(true);
    try {
      await page.goto('/');
      const catTab = page.locator('.products-finder .tab-bar .tab', { hasText: CAT });
      await catTab.click();
      await expect(tile(page, C.name)).toBeVisible();
      await enterJiggle(page, tile(page, C.name));
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/buttons/hide') && r.ok()),
        cellOf(page, C.name).getByTestId('tile-badge-hide').click(),
      ]);
      // Still editing, still on the category (its greyed tiles stay reachable).
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(catTab).toBeVisible();
      await expect(catTab).toHaveClass(/active/);
      await expect(cellOf(page, C.name)).toHaveClass(/tile--hidden/);

      await page.getByTestId('jiggle-done').click();
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(catTab).toBeHidden();
      const active = page.locator('.products-finder .tab-bar .tab[data-cat-tab].active');
      await expect(active).toHaveCount(1);
      await expect(active).toBeVisible();
      await expect(page.locator('#buttons-grid .products-tab-panel:visible [data-testid="category-empty-state"]:visible')).toHaveCount(0);
      await expect(page.locator('#buttons-grid .products-tab-panel:visible .btn-tile[data-code]:visible').first()).toBeVisible();
    } finally {
      for (const id of [idA, idB, idC]) await page.request.post('/api/buttons/unhide', { form: { itemId: id } });
    }
    assertClean();
  });

  test('the Designer shows a hidden tile greyed at rest', async ({ page }) => {
    const assertClean = watchConsole(page);
    const idA = (await itemId(page, A.name))!;
    expect((await page.request.post('/api/buttons/hide', { form: { itemId: idA } })).ok()).toBe(true);
    try {
      await page.goto('/designer');
      await page.getByRole('tab', { name: CAT }).click();
      const t = page.locator(`.designer-preview .btn-tile[data-name="${A.name}"]`);
      await expect(t).toBeVisible();
      await expect(t.locator('xpath=..')).toHaveClass(/tile--hidden/);
      await expect(page.getByTestId(`designer-hidden-item-${idA}`)).toBeVisible();
      const shotDir = process.env.UT_SHOT_DIR;
      await page.screenshot({ path: shotDir ? `${shotDir}/2698-designer-greyed.png` : test.info().outputPath('designer-greyed.png') });
    } finally {
      await page.request.post('/api/buttons/unhide', { form: { itemId: idA } });
    }
    assertClean();
  });
});
