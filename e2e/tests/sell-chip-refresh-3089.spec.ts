import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#3089: the all_filter_chips category chip used to live only in
// Alpine x-data, so the .products root's own whole-document refresh
// (hx-trigger="modifiers-changed from:body, buttons-changed from:body" —
// buttons.html's own top comment) always swapped the grid back to All
// mid-sale, dropping whatever category chip the cashier had tapped. GET
// /ui/buttons now accepts the same ?category= the chip's own hx-get
// already sends (ButtonsHTTP.AllMore's semantics), so the refresh renders
// that same chip pressed and the grid already filtered — no flash of All.
//
// Reuses sell-screen-browsing-mode-2499.spec.ts's own createChipCategories
// helper shape (categories with no quick button — the chip mode browses
// the catalog itself, so nothing needs a button to earn a chip).

type Cat = { id: string; name: string; itemId: string; itemName: string };
const RUN = Date.now().toString(36).toUpperCase();

async function createChipCategories(page: Page, tag: string, count: number): Promise<Cat[]> {
  const catNames = Array.from({ length: count }, (_, i) => `Chip3089 ${tag} ${RUN} ${String(i + 1).padStart(2, '0')}`);
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

// Fires the exact refresh the .products root wires up (see this file's own
// top comment) and waits for the resulting GET /ui/buttons response — never
// AllMore's own /ui/buttons/all/more, which a chip tap also triggers.
// Tags the current .products root before firing and waits until the
// outerHTML swap has replaced it, so every assertion afterwards reads the
// NEW DOM -- a response arriving is not yet a swap (review finding 5).
// Returns the refresh request's URL so a caller can check what it carried.
async function fireButtonsChangedAndWait(page: Page): Promise<string> {
  await page.evaluate(() => { (document.querySelector('.products') as HTMLElement).dataset.stale = '1'; });
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/ui/buttons') && !r.url().includes('/ui/buttons/all/more')),
    page.evaluate(() => document.body.dispatchEvent(new Event('buttons-changed', { bubbles: true }))),
  ]);
  await expect(page.locator('.products[data-stale]')).toHaveCount(0);
  return resp.url();
}

test.describe('Sell screen chip survives a buttons-changed refresh (ut-docs#3089)', () => {
  let chipCats: Cat[] = [];

  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow'); // never leave another mode for the next spec file's worker
    if (chipCats.length) await deactivate(page, chipCats);
    chipCats = [];
    await page.request.post('/api/pos/reset');
  });

  test('a picked chip and its filtered grid survive a buttons-changed refresh; a category that is gone falls back to All quietly', async ({ page }) => {
    const assertClean = watchConsole(page);
    chipCats = await createChipCategories(page, 'A', 3);
    await setBrowsingMode(page, 'all_filter_chips');

    await page.goto('/');
    const chips = page.locator('#browsing-category-chips');
    await expect(chips).toBeVisible();
    const allChip = chips.locator('[data-cat-all]');
    let grid = page.locator('#buttons-grid-all');
    const target = chipCats[1];
    const other = chipCats[0];
    let targetChip = chips.locator(`.chip[data-cat-id="${target.id}"]`);

    // Pick a chip.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes(`category=${target.id}`)),
      targetChip.click(),
    ]);
    await expect(targetChip).toHaveAttribute('aria-pressed', 'true');
    await expect(allChip).toHaveAttribute('aria-pressed', 'false');
    await expect(grid.locator('.btn-tile', { hasText: target.itemName })).toBeVisible();
    await expect(grid.locator('.btn-tile', { hasText: other.itemName })).toHaveCount(0);

    // Fire buttons-changed (a tile move/remove/add elsewhere, or a
    // modifiers-changed config edit, re-fetches this exact same root) --
    // the chip and the filtered grid must survive it, with no flash of All.
    const refreshURL = await fireButtonsChangedAndWait(page);
    expect(new URL(refreshURL).searchParams.get('category')).toBe(target.id);

    // The root was outerHTML-swapped, so re-query fresh locators.
    grid = page.locator('#buttons-grid-all');
    const chipsAfter = page.locator('#browsing-category-chips');
    targetChip = chipsAfter.locator(`.chip[data-cat-id="${target.id}"]`);
    await expect(targetChip).toHaveAttribute('aria-pressed', 'true');
    await expect(chipsAfter.locator('[data-cat-all]')).toHaveAttribute('aria-pressed', 'false');
    await expect(grid.locator('.btn-tile', { hasText: target.itemName })).toBeVisible();
    await expect(grid.locator('.btn-tile', { hasText: other.itemName })).toHaveCount(0);
    // The filter marker jiggle-mode keys drag off must have survived too.
    await expect(grid.locator(`[data-all-filter="${target.id}"]`)).toHaveCount(1);

    // Now the selected category goes away entirely: deactivate its one
    // item (so the category-active toggle below is no longer blocked),
    // then deactivate the category itself.
    await page.request.post('/api/catalog/item/deactivate', { form: { id: target.itemId } });
    const deactivateResp = await page.request.post(`/api/categories/${target.id}/active`, { form: { active: '0' } });
    expect(deactivateResp.ok(), 'deactivate the target category').toBe(true);
    chipCats = chipCats.filter((c) => c.id !== target.id); // already gone -- afterEach's own cleanup skips it

    // A refresh with the (now gone) chip still selected client-side must
    // fall back to All quietly: no error, the chip disappears from the
    // row, All renders pressed, and the rest of the catalog is back.
    await fireButtonsChangedAndWait(page);
    const chipsFinal = page.locator('#browsing-category-chips');
    await expect(chipsFinal.locator('[data-cat-all]')).toHaveAttribute('aria-pressed', 'true');
    await expect(chipsFinal.locator(`.chip[data-cat-id="${target.id}"]`)).toHaveCount(0);
    await expect(page.locator('#buttons-grid-all').locator('.btn-tile', { hasText: other.itemName })).toBeVisible();

    assertClean();
  });
});
