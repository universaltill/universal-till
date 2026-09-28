import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#3074 (product owner, binding UI rule 3 — "be like an app, not a
// web page"): on the Quick Buttons Designer (/designer), a category drop
// used to fire buttons-changed, which outerHTML-swapped the whole .products
// root (strip + grid + category list) and left the operator somewhere else
// on the page. Now a successful reorder updates only what moved: the list
// is already in its new DOM order, the strip's tabs are permuted in place,
// and the per-row Move earlier/later disabled state is re-derived. Same for
// Move earlier/later inside a row's open form (utListReorder.move — the
// row stays put with its form open, no re-render) and for a tile drop in
// jiggle mode (app.js utTileJiggle persistOrder, already in place).
//
// Each case measures, around the action: every scroll offset on the page
// (unchanged), main-frame navigations (none), GET /ui/buttons re-fetches
// (none), and the identity of the .products root (the SAME node, tagged
// with a JS expando before the action — a swap would drop it). Every order
// is also checked AFTER A RELOAD: the DOM moves before the request, so
// "the rows moved" alone would pass against a refused save (ut-docs#2018).
//
// HONESTY NOTE: pointer gestures are Playwright's synthetic mouse
// (pointerType "mouse") in desktop Chromium, as in
// category-list-drag-2699.spec.ts — not a finger on the pilot till.

const RUN = Date.now().toString(36).toUpperCase();

function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  const cats = [`NoReload3074 ${run} A`, `NoReload3074 ${run} B`, `NoReload3074 ${run} C`];
  const items = [
    { name: `NR3074 Item A1 ${run}`, sku: `NR3074A1${run}`, barcode: `NR3074-A1-${run}`, category: cats[0] },
    { name: `NR3074 Item A2 ${run}`, sku: `NR3074A2${run}`, barcode: `NR3074-A2-${run}`, category: cats[0] },
    { name: `NR3074 Item B1 ${run}`, sku: `NR3074B1${run}`, barcode: `NR3074-B1-${run}`, category: cats[1] },
    { name: `NR3074 Item C1 ${run}`, sku: `NR3074C1${run}`, barcode: `NR3074-C1-${run}`, category: cats[2] },
  ];
  return { run, cats, items };
}
type Fixture = ReturnType<typeof fixture>;

// Same seeding as designer-wysiwyg-2174.spec.ts: a catalog import creates
// the items and their categories, then each item becomes a quick button so
// every category has a strip tab.
async function seed(page: Page, f: Fixture) {
  await setBrowsingMode(page, 'strip_overflow');
  const csv = 'Name,SKU,Barcode,Price,Category,In stock\n' +
    f.items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: 'import-3074.csv', mimeType: 'text/csv', buffer: Buffer.from(csv) });
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
  await page.unrouteAll({ behavior: 'ignoreErrors' });
  for (const it of f.items) await page.request.post('/api/buttons/remove', { form: { code: it.barcode } });
  await page.goto('/catalog');
  for (const it of f.items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

const catRow = (page: Page, name: string) =>
  page.locator('.designer-cat', { has: page.locator('.designer-cat-name', { hasText: name }) });

async function openDesigner(page: Page, f: Fixture): Promise<string[]> {
  // A short viewport so the Designer is long enough to scroll.
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/designer');
  for (const c of f.cats) await expect(catRow(page, c)).toHaveCount(1);
  const ids: string[] = [];
  for (const c of f.cats) ids.push((await catRow(page, c).getAttribute('data-cat-id'))!);
  return ids;
}

// Every scroll offset on the page, keyed by a stable description of its
// element (never an index — the DOM may legitimately change around it).
async function scrollState(page: Page): Promise<Record<string, number>> {
  return page.evaluate(() => {
    const out: Record<string, number> = {};
    const docEl = document.scrollingElement || document.documentElement;
    out.document = docEl.scrollTop;
    document.querySelectorAll('body *').forEach((el) => {
      if (el.scrollTop > 0) out[`${el.tagName}#${el.id}.${el.className}`] = el.scrollTop;
    });
    return out;
  });
}

// Centre `el` in the viewport ONCE, before measuring, so nothing the test
// then clicks needs Playwright's own scroll-into-view.
async function centre(el: Locator) {
  await el.evaluate((n) => n.scrollIntoView({ block: 'center', inline: 'nearest' }));
  await el.page().waitForTimeout(100);
}

type Watch = { nav: number; refetch: number; stop: () => void };
async function watch(page: Page): Promise<Watch> {
  await page.evaluate(() => { (document.querySelector('.products') as unknown as { __ut3074?: number }).__ut3074 = 1; });
  const w: Watch = { nav: 0, refetch: 0, stop: () => {} };
  const onNav = (fr: import('@playwright/test').Frame) => { if (fr === page.mainFrame()) w.nav++; };
  const onReq = (r: import('@playwright/test').Request) => {
    if (r.method() === 'GET' && new URL(r.url()).pathname === '/ui/buttons') w.refetch++;
  };
  page.on('framenavigated', onNav);
  page.on('request', onReq);
  w.stop = () => { page.off('framenavigated', onNav); page.off('request', onReq); };
  return w;
}

async function expectInPlace(page: Page, w: Watch, scrollBefore: Record<string, number>) {
  // Give a (wrong) buttons-changed re-fetch time to start and land.
  await page.waitForTimeout(700);
  w.stop();
  expect(w.nav, 'no navigation').toBe(0);
  expect(w.refetch, 'no GET /ui/buttons re-fetch of the replica').toBe(0);
  expect(await page.evaluate(() => (document.querySelector('.products') as unknown as { __ut3074?: number }).__ut3074),
    'the .products root is the same node (no outerHTML swap)').toBe(1);
  expect(await scrollState(page), 'scroll position unchanged').toEqual(scrollBefore);
}

async function listOrder(page: Page, ids: string[]): Promise<string[]> {
  const all = await page.locator('.designer-cat-list > [data-reorder-id]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('data-reorder-id') || ''));
  return all.filter((id) => ids.includes(id));
}
async function stripOrder(page: Page, ids: string[]): Promise<string[]> {
  const all = await page.locator('.products .tab-bar [data-cat-tab]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('data-tab-id') || ''));
  return all.filter((id) => ids.includes(id));
}
// Each row's Move earlier is disabled iff it is first, Move later iff last.
async function moveButtonsConsistent(page: Page): Promise<string[]> {
  return page.locator('.designer-cat-list').evaluate((list) => {
    const rows = Array.from(list.querySelectorAll(':scope > [data-reorder-id]'));
    const bad: string[] = [];
    rows.forEach((r, i) => {
      const up = r.querySelector('[data-cat-move="-1"]') as HTMLButtonElement | null;
      const down = r.querySelector('[data-cat-move="1"]') as HTMLButtonElement | null;
      if (up && up.disabled !== (i === 0)) bad.push(`${r.getAttribute('data-reorder-id')} up`);
      if (down && down.disabled !== (i === rows.length - 1)) bad.push(`${r.getAttribute('data-reorder-id')} down`);
    });
    return bad;
  });
}

const reorderSaved = (page: Page) => page.waitForResponse(
  (r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');

test.describe('Designer: a drop updates in place, no reload (ut-docs#3074)', () => {
  test('category long-press drag: same root, same scroll, strip follows, persisted', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('D');
    await seed(page, f);
    try {
      const [a, b, c] = await openDesigner(page, f);
      expect(await listOrder(page, [a, b, c])).toEqual([a, b, c]);
      expect(await stripOrder(page, [a, b, c])).toEqual([a, b, c]);
      await centre(catRow(page, f.cats[1]));
      const before = await scrollState(page);
      expect(Object.values(before).some((v) => v > 0), `the page is scrolled: ${JSON.stringify(before)}`).toBe(true);
      const w = await watch(page);

      // Long-press C's row body, drag it above A.
      const from = (await catRow(page, f.cats[2]).locator('.designer-cat-open').boundingBox())!;
      const over = (await catRow(page, f.cats[0]).boundingBox())!;
      const saved = reorderSaved(page);
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
      await page.mouse.down();
      await page.waitForTimeout(650);
      await page.mouse.move(from.x + from.width / 2, over.y + 3, { steps: 20 });
      await page.mouse.up();
      expect((await saved).status()).toBe(204);

      await expectInPlace(page, w, before);
      expect(await listOrder(page, [a, b, c])).toEqual([c, a, b]);
      await expect.poll(() => stripOrder(page, [a, b, c]), 'strip tabs follow the new order').toEqual([c, a, b]);
      expect(await moveButtonsConsistent(page)).toEqual([]);
      await expect(catRow(page, f.cats[2]).locator('.designer-cat-form')).toBeHidden();

      await page.reload();
      await expect(catRow(page, f.cats[2])).toHaveCount(1);
      expect(await listOrder(page, [a, b, c])).toEqual([c, a, b]);
      expect(await stripOrder(page, [a, b, c])).toEqual([c, a, b]);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('Move later / earlier in the open form: row stays open, focus kept, same root and scroll, persisted', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('M');
    await seed(page, f);
    try {
      const [a, b, c] = await openDesigner(page, f);
      await catRow(page, f.cats[0]).locator('.designer-cat-open').click();
      const down = page.locator(`#designer-cat-down-${a}`);
      const up = page.locator(`#designer-cat-up-${a}`);
      await expect(down).toBeVisible();
      await centre(down);
      const before = await scrollState(page);
      expect(Object.values(before).some((v) => v > 0), `the page is scrolled: ${JSON.stringify(before)}`).toBe(true);
      const w = await watch(page);

      let saved = reorderSaved(page);
      await down.click();
      expect((await saved).status()).toBe(204);
      expect(await listOrder(page, [a, b, c])).toEqual([b, a, c]);
      await expect(page.locator(`#designer-cat-form-${a}`)).toBeVisible();
      await expect(down).toBeFocused();
      await expect.poll(() => stripOrder(page, [a, b, c])).toEqual([b, a, c]);
      expect(await moveButtonsConsistent(page)).toEqual([]);

      // And back up, with the keyboard on the other button.
      await up.focus();
      saved = reorderSaved(page);
      await page.keyboard.press('Enter');
      expect((await saved).status()).toBe(204);
      expect(await listOrder(page, [a, b, c])).toEqual([a, b, c]);
      // Focus stays on the button -- unless A just became the whole list's
      // first row (seed order vs. earlier runs' categories decides that),
      // where Move earlier is now disabled and focus moves to its twin in
      // the same form rather than dropping to <body>.
      await expect(await up.isDisabled() ? down : up).toBeFocused();
      await expect.poll(() => stripOrder(page, [a, b, c])).toEqual([a, b, c]);
      // And once more later, to persist a changed order.
      await down.focus();
      saved = reorderSaved(page);
      await page.keyboard.press('Enter');
      expect((await saved).status()).toBe(204);
      await expect(page.locator(`#designer-cat-form-${a}`)).toBeVisible();

      await expectInPlace(page, w, before);
      expect(await moveButtonsConsistent(page)).toEqual([]);

      await page.reload();
      await expect(catRow(page, f.cats[0])).toHaveCount(1);
      expect(await listOrder(page, [a, b, c])).toEqual([b, a, c]);
      expect(await stripOrder(page, [a, b, c])).toEqual([b, a, c]);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('the last row: Move later disabled, Move earlier re-enables it and disables the new last row', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('E');
    await seed(page, f);
    try {
      await openDesigner(page, f);
      const rows = page.locator('.designer-cat-list > [data-reorder-id]');
      const n = await rows.count();
      const lastId = await rows.nth(n - 1).getAttribute('data-reorder-id');
      const prevId = await rows.nth(n - 2).getAttribute('data-reorder-id');
      await page.locator(`#designer-cat-open-${lastId}`).click();
      await expect(page.locator(`#designer-cat-down-${lastId}`)).toBeDisabled();
      const saved = reorderSaved(page);
      await page.locator(`#designer-cat-up-${lastId}`).click();
      expect((await saved).status()).toBe(204);
      await expect(page.locator(`#designer-cat-down-${lastId}`)).toBeEnabled();
      await expect(page.locator(`#designer-cat-down-${prevId}`)).toBeDisabled();
      expect(await moveButtonsConsistent(page)).toEqual([]);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('a refused move puts the row back, leaves the strip alone and says why — still in place', async ({ page }) => {
    const assertClean = watchConsole(page, /409/);
    const f = fixture('F');
    await seed(page, f);
    try {
      const [a, b, c] = await openDesigner(page, f);
      await page.route('**/api/designer/categories/reorder', (route) =>
        route.fulfill({ status: 409, contentType: 'text/html', body: '<div class="error">Manage categories on the main till.</div>' }));
      await catRow(page, f.cats[0]).locator('.designer-cat-open').click();
      const down = page.locator(`#designer-cat-down-${a}`);
      await centre(down);
      const before = await scrollState(page);
      const w = await watch(page);
      await down.click();
      await expect.poll(() => listOrder(page, [a, b, c])).toEqual([a, b, c]);
      await expect(page.locator('#designer-categories-msg')).toContainText('main till');
      expect(await stripOrder(page, [a, b, c])).toEqual([a, b, c]);
      expect(await moveButtonsConsistent(page)).toEqual([]);
      await expect(page.locator(`#designer-cat-form-${a}`)).toBeVisible();
      await expectInPlace(page, w, before);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });

  test('tile drag in jiggle mode + Done: same root, same scroll, persisted', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('T');
    await seed(page, f);
    try {
      const [a] = await openDesigner(page, f);
      await page.locator(`#cat-tab-${a}`).click();
      const t1 = page.locator(`#buttons-grid .btn-tile[data-code="${f.items[0].barcode}"]`);
      const t2 = page.locator(`#buttons-grid .btn-tile[data-code="${f.items[1].barcode}"]`);
      await expect(t1).toBeVisible();
      await centre(t1);
      // Right-click enters jiggle mode (keyboard/pointer-equivalent entry).
      await t1.click({ button: 'right' });
      const done = page.locator('[data-testid="jiggle-done"]');
      await expect(done).toBeVisible();
      await expect(done).toBeInViewport();
      const before = await scrollState(page);
      expect(Object.values(before).some((v) => v > 0), `the page is scrolled: ${JSON.stringify(before)}`).toBe(true);
      const w = await watch(page);

      const from = (await t1.boundingBox())!;
      const to = (await t2.boundingBox())!;
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
      await page.mouse.down();
      await page.mouse.move(from.x + from.width / 2 + 6, from.y + from.height / 2 + 2, { steps: 3 });
      await page.mouse.move(to.x + to.width * 0.8, to.y + to.height / 2, { steps: 25 });
      await page.mouse.up();
      const saved = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST');
      const db = (await done.boundingBox())!;
      await page.mouse.click(db.x + db.width / 2, db.y + db.height / 2);
      expect((await saved).ok()).toBe(true);

      await expectInPlace(page, w, before);
      const codes = await page.locator(`#buttons-grid .btn-tile[data-code^="NR3074-A"]`).evaluateAll(
        (els) => els.map((e) => (e as HTMLElement).dataset.code));
      expect(codes).toEqual([f.items[1].barcode, f.items[0].barcode]);

      await page.reload();
      await page.locator(`#cat-tab-${a}`).click();
      await expect(page.locator(`#buttons-grid .btn-tile[data-code="${f.items[0].barcode}"]`)).toBeVisible();
      const after = await page.locator(`#buttons-grid .btn-tile[data-code^="NR3074-A"]`).evaluateAll(
        (els) => els.map((e) => (e as HTMLElement).dataset.code));
      expect(after).toEqual([f.items[1].barcode, f.items[0].barcode]);
      assertClean();
    } finally {
      await cleanup(page, f);
    }
  });
});
