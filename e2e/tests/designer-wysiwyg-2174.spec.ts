import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2174: /designer is a live, in-place-edited replica of the sale
// screen's real product panel — the SAME buttons.html fragment the sale
// screen renders (GET /ui/buttons, fetched with ?mode=edit via hx-vals), so
// the category strip, the tile grid and the jiggle edit mode (ut-docs#2339,
// app.js utTileJiggle) are the real thing, not a fork — plus a
// category-management section (create / rename+recolour / reorder /
// deactivate) wired to /api/designer/categories*, each of which answers
// 204 + HX-Trigger: buttons-changed so the replica refreshes in place.
//
// This replaces designer-reorder-1221.spec.ts and
// designer-reorder-buttons-overflow-1354.spec.ts, both of which pinned the
// retired flat admin grid (#buttons-grid-admin, per-tile ▲/▼/✕ buttons)
// that no longer exists.
//
// HONESTY NOTE: written in a cycle with no browser available (ut-docs#2174's
// Dev lane), so this file has NOT been run before hand-off — the Tester lane
// runs it first. Pointer gestures are Playwright's synthetic mouse, as in
// sell-tile-jiggle-mode-2339.spec.ts; the keyboard paths are the point here
// (ut-docs#826: a pointer-only interaction is a bug).

const RUN = Date.now().toString(36).toUpperCase();
function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  const cat = `Wysiwyg2174 Cat ${run}`;
  return {
    run,
    cat,
    A: { name: `Wysiwyg2174 Item A ${run}`, sku: `WYS2174A${run}`, barcode: `WYS2174BC-A-${run}`, category: cat },
    B: { name: `Wysiwyg2174 Item B ${run}`, sku: `WYS2174B${run}`, barcode: `WYS2174BC-B-${run}`, category: cat },
  };
}
type Item = ReturnType<typeof fixture>['A'];

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Same seeding as sell-tile-jiggle-mode-2339.spec.ts: a catalog import
// creates the items (and their category), then each becomes a quick button
// via /api/buttons/add so it renders as a tile.
async function seedItems(page: Page, items: Item[]) {
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

const replica = (page: Page) => page.locator('#buttons-grid');
const tile = (page: Page, it: Item) => page.locator(`[data-testid="designer-tile"][data-code="${it.barcode}"]`);
const catRow = (page: Page, name: string) => page.locator('.designer-cat', { has: page.locator('.designer-cat-name', { hasText: name }) });

test.describe('Quick Buttons Designer as a live sale-screen replica (ut-docs#2174)', () => {
  test('replica renders the real panel with inert tiles; jiggle keyboard reorder works on it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('1');
    await seedItems(page, [f.A, f.B]);
    try {
      await page.goto('/designer');

      // The real panel: same ids/classes app.js and Alpine key off on the
      // sale screen, fetched in edit mode.
      const root = page.locator('.products[hx-get="/ui/buttons"]');
      await expect(root).toHaveAttribute('hx-vals', '{"mode":"edit"}');
      await expect(replica(page)).toBeVisible();
      await expect(tile(page, f.A)).toBeVisible();
      await expect(tile(page, f.B)).toBeVisible();
      // The seeded category is a real strip tab here, exactly as on /.
      await expect(page.locator('[role=tab][data-cat-tab]', { hasText: f.cat })).toBeVisible();
      // Sale-only affordances are gone.
      await expect(page.locator('#products-search')).toHaveCount(0);
      await expect(page.locator('a.products-strip-edit')).toHaveCount(0);
      await expect(page.locator('#cat-tab-all')).toHaveCount(0);

      // (1) Inert tiles: a tap never reaches the cashier's basket.
      const scans: string[] = [];
      page.on('request', (r) => { if (r.url().includes('/api/pos/scan')) scans.push(r.url()); });
      await tile(page, f.A).click();
      await page.waitForTimeout(400);
      expect(scans, 'a Designer tile must never post /api/pos/scan').toHaveLength(0);

      // (2) Jiggle edit mode by keyboard-reachable means (right-click
      // enters; ArrowLeft moves the focused tile; Done persists once).
      const orderBefore = await replica(page)
        .locator(`.btn-tile[data-code^="WYS2174BC-"][data-code$="-${f.run}"]`)
        .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code));
      expect(orderBefore).toEqual([f.A.barcode, f.B.barcode]);

      await tile(page, f.B).click({ button: 'right' });
      await expect(page.locator('[data-testid="jiggle-done"]')).toBeVisible();
      await expect(replica(page)).toHaveClass(/jiggle-mode/);
      await tile(page, f.B).focus();
      await page.keyboard.press('ArrowLeft');
      const reorder = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST');
      await page.locator('[data-testid="jiggle-done"]').click();
      expect((await reorder).ok()).toBe(true);

      // The persisted order is what a fresh render shows.
      await page.reload();
      await expect(tile(page, f.A)).toBeVisible();
      const orderAfter = await replica(page)
        .locator(`.btn-tile[data-code^="WYS2174BC-"][data-code$="-${f.run}"]`)
        .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code));
      expect(orderAfter).toEqual([f.B.barcode, f.A.barcode]);

      // (3) The edit badge returns here, not to the sale screen.
      await expect(page.locator(`.tile-badge-edit[href$="&return=/designer"]`).first()).toBeAttached();

      // (4) A category with active items says up front that deactivating
      // it is blocked (and the bin badge is wired to that explanation).
      const row = catRow(page, f.cat);
      await expect(row).toBeVisible();
      await expect(row.locator('[data-testid^="designer-cat-blocked-"]')).toBeVisible();
    } finally {
      await cleanupItems(page, [f.A, f.B]);
    }
    assertClean();
  });

  test('category create / rename / keyboard reorder with focus kept / deactivate / reactivate, all in place', async ({ page }) => {
    const assertClean = watchConsole(page);
    const f = fixture('2');
    const newName = `Wysiwyg2174 New ${f.run}`;
    const renamed = `${newName} Renamed`;
    page.on('dialog', (d) => d.accept());

    await page.goto('/designer');
    await expect(replica(page)).toBeVisible();

    // Create: the "+ New category" button is a real button that discloses a
    // form; the create answers 204 + buttons-changed and the row appears
    // with no navigation.
    const newBtn = page.locator('#designer-cat-new');
    await expect(newBtn).toHaveAttribute('aria-expanded', 'false');
    await newBtn.click();
    await expect(newBtn).toHaveAttribute('aria-expanded', 'true');
    const newForm = page.locator('#designer-cat-form-new');
    await newForm.locator('input[name="name"]').fill(newName);
    await newForm.locator('input[type="radio"][name="color"]').nth(1).check();
    const created = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories') && r.request().method() === 'POST');
    await newForm.locator('#designer-cat-new-submit').click();
    expect((await created).status()).toBe(204);
    expect(page.url()).toContain('/designer');
    const row = catRow(page, newName);
    await expect(row).toBeVisible();
    const id = (await row.getAttribute('data-cat-id'))!;
    // No quick buttons yet, so — exactly as on the sale screen — no tab.
    await expect(page.locator(`#cat-tab-${id}`)).toHaveCount(0);
    await expect(row.locator('.designer-cat-meta')).toContainText('0 button');

    // Rename (+ recolour) in place.
    await row.locator('[data-cat-edit]').click();
    const form = page.locator(`#designer-cat-form-${id}`);
    await expect(form).toBeVisible();
    await form.locator('input[name="name"]').fill(renamed);
    await form.locator('input[type="radio"][name="color"][value=""]').check();
    const updated = page.waitForResponse((r) => r.url().endsWith(`/api/designer/categories/${id}`) && r.request().method() === 'POST');
    await form.locator(`#designer-cat-save-${id}`).click();
    expect((await updated).status()).toBe(204);
    await expect(catRow(page, renamed)).toBeVisible();
    await expect(catRow(page, newName).locator('.designer-cat-name', { hasText: /Renamed$/ })).toHaveCount(1);

    // Reorder by keyboard: the new category is last, so "move later" is
    // disabled and "move earlier" works from the keyboard — and after the
    // replica re-renders, focus is back on the same control (not <body>).
    // ut-docs#2699: the pair lives in the row's inline form now.
    await page.locator(`#designer-cat-open-${id}`).click();
    const up = page.locator(`#designer-cat-up-${id}`);
    await expect(page.locator(`#designer-cat-down-${id}`)).toBeDisabled();
    await up.focus();
    const reordered = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');
    await page.keyboard.press('Enter');
    expect((await reordered).status()).toBe(204);
    await expect(page.locator(`#designer-cat-down-${id}`)).toBeEnabled();
    await expect.poll(() => page.evaluate(() => document.activeElement && document.activeElement.id)).toBe(`designer-cat-up-${id}`);
    const ids = await page.locator('.designer-cat').evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.catId));
    expect(ids.indexOf(id)).toBe(ids.length - 2);

    // Deactivate (empty category: allowed, after the confirm) → the row
    // offers Activate; reactivate → it offers the bin again.
    const deactivated = page.waitForResponse((r) => r.url().endsWith(`/api/designer/categories/${id}/active`) && r.request().method() === 'POST');
    await page.locator(`#designer-cat-active-${id}`).click();
    expect((await deactivated).status()).toBe(204);
    await expect(catRow(page, renamed)).toHaveClass(/designer-cat-inactive/);
    await expect(page.locator(`#designer-cat-active-${id}`)).toHaveText(/Activate/);
    const reactivated = page.waitForResponse((r) => r.url().endsWith(`/api/designer/categories/${id}/active`) && r.request().method() === 'POST');
    await page.locator(`#designer-cat-active-${id}`).click();
    expect((await reactivated).status()).toBe(204);
    await expect(catRow(page, renamed)).not.toHaveClass(/designer-cat-inactive/);

    // Leave it deactivated so the shared demo till's strip is untouched.
    await page.request.post(`/api/designer/categories/${id}/active`, { form: { active: '0' } });
    assertClean();
  });
});
