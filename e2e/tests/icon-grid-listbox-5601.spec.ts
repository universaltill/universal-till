import path from 'path';
import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5601 (follow-up to #5553): the built-in icon grids (catalog item
// image, category dialog) are single-select ARIA listboxes with grouped
// options (APG "Listbox with Grouped Options"), one Tab stop, and FOCUS-ONLY
// arrow keys (web/public/radiogroup.js, data-roving-listbox): arrows never
// choose, Enter/Space/click does. That is what keeps arrowing over the
// catalog grid from POSTing /api/catalog/item/icon once per keypress and
// replacing an uploaded photo. The keyboard tests fail with the listbox
// branch of radiogroup.js removed; the role/markup tests and the
// category-photo test check the template, not that branch.
//
// HONESTY NOTE: Chromium with synthetic keys; NVDA/JAWS/VoiceOver output is
// reasoned from the APG pattern, not heard.

const RUN = Date.now().toString(36).toUpperCase();
const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const CAT = '#category-icon-grid';
const ITEM = '#builtin-icon-grid';
const CAT_SEARCH = 'input[data-icon-filter="category-icon-grid"]';
const ITEM_SEARCH = 'input[data-icon-filter="builtin-icon-grid"]';
const ICON_PNG = path.join(__dirname, '../fixtures/test-item-image.png');

async function openCategory(page: Page, query = '') {
  await page.goto(`/categories${query}`);
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
}

// A test-own item with an uploaded photo, opened on its image tab.
async function openItemWithPhoto(page: Page, name: string, query = '') {
  const created = await page.request.post('/api/catalog/item', { form: { name, price: '199' } });
  expect(created.ok()).toBeTruthy();
  await page.goto(`/catalog${query}`);
  await page.locator('.catalog-row', { hasText: name }).first().click();
  await page.locator('#item-form-tab-image').click();
  await page.locator('#image-file').setInputFiles(ICON_PNG);
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');
}

const focusedIcon = (page: Page) =>
  page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.icon ?? '');
const inGrid = (page: Page, sel: string) =>
  page.evaluate((s) => !!document.activeElement?.closest(s), sel);

test.describe('icon grids are focus-only listboxes (ut-docs#5601)', () => {
  test('category dialog: listbox roles, groups labelled by their headings, names', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openCategory(page);
    const grid = page.locator(CAT);
    await expect(grid).toHaveAttribute('role', 'listbox');
    await expect(grid).toHaveAttribute('data-roving-listbox', '');
    expect(((await grid.getAttribute('aria-label')) ?? '').length).toBeGreaterThan(0);
    await expect(grid.locator('[aria-pressed]')).toHaveCount(0);
    const tiles = grid.locator('.builtin-icon-tile');
    const n = await tiles.count();
    expect(n).toBeGreaterThan(60);
    for (let i = 0; i < n; i++) {
      await expect(tiles.nth(i)).toHaveAttribute('role', 'option');
      await expect(tiles.nth(i)).toHaveAttribute('aria-selected', /^(true|false)$/);
    }
    const groups = grid.locator('[role="group"]');
    await expect(groups).toHaveCount(10);
    for (let i = 0; i < 10; i++) {
      const id = await groups.nth(i).getAttribute('aria-labelledby');
      expect(id).toBeTruthy();
      const heading = page.locator(`[id="${id}"]`);
      expect(((await heading.textContent()) ?? '').trim().length).toBeGreaterThan(0);
      await expect(heading).not.toHaveAttribute('tabindex', /.*/);
    }
    // Accessible name == the tile's visible label.
    const coffee = grid.locator('[data-icon="coffee"]');
    await expect(coffee).toHaveAccessibleName(((await coffee.locator('span').textContent()) ?? '').trim());
    assertClean();
  });

  test('catalog grid: same listbox roles, no aria-pressed', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    const grid = page.locator(ITEM);
    await expect(grid).toHaveAttribute('role', 'listbox');
    await expect(grid).toHaveAttribute('data-roving-listbox', '');
    await expect(grid.locator('[aria-pressed]')).toHaveCount(0);
    await expect(grid.locator('[role="group"]')).toHaveCount(10);
    await expect(grid.locator('.builtin-icon-tile:not([role="option"])')).toHaveCount(0);
    assertClean();
  });

  for (const which of ['category', 'catalog'] as const) {
    test(`${which}: exactly one Tab stop in every state`, async ({ page }) => {
      const assertClean = watchConsole(page);
      let grid: string, search: string;
      if (which === 'category') {
        grid = CAT; search = CAT_SEARCH;
        await openCategory(page);
      } else {
        grid = ITEM; search = ITEM_SEARCH;
        await openItemWithPhoto(page, `Listbox5601 Tab ${RUN}`);
      }
      const stops = page.locator(`${grid} [tabindex="0"]`);
      await expect(stops).toHaveCount(1); // photo (catalog) / new (category): nothing selected
      const g = page.locator(grid);
      // Nothing selected -> the tab stop is the first option, "No image".
      await expect(g.locator('[data-icon="none"]')).toHaveAttribute('tabindex', '0');

      // An icon chosen -> that tile is the stop; Shift+Tab out, Tab back, Tab out.
      const coffee = g.locator('[data-icon="coffee"]');
      await coffee.click();
      await expect(coffee).toHaveAttribute('aria-selected', 'true');
      await expect(stops).toHaveCount(1);
      await expect(coffee).toHaveAttribute('tabindex', '0');
      await coffee.focus();
      await page.keyboard.press('Shift+Tab');
      expect(await inGrid(page, grid)).toBe(false);
      await page.keyboard.press('Tab');
      await expect(coffee).toBeFocused();
      await page.keyboard.press('Tab');
      expect(await inGrid(page, grid)).toBe(false);

      // Filter hides the selected tile: still exactly one reachable stop.
      await page.locator(search).fill('wine');
      await expect(stops).toHaveCount(1);
      await expect(page.locator(`${grid} [tabindex="0"]:not([hidden])`)).toHaveCount(1);
      await page.locator(search).focus();
      await page.keyboard.press('Tab');
      expect(await inGrid(page, grid)).toBe(true);
      await page.keyboard.press('Tab');
      expect(await inGrid(page, grid)).toBe(false);
      assertClean();
    });
  }

  test('category: category photo picked leaves one stop and no selected tile', async ({ page }) => {
    await openCategory(page);
    await page.locator('#category-image-file').setInputFiles(ICON_PNG);
    await expect(page.locator(`${CAT} [aria-selected="true"]`)).toHaveCount(0);
    await expect(page.locator(`${CAT} [tabindex="0"]`)).toHaveCount(1);
    await expect(page.locator(`${CAT} [data-icon="none"]`)).toHaveAttribute('tabindex', '0');
  });

  test('arrows cross sections, wrap, Home/End, never change selection', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openCategory(page);
    const grid = page.locator(CAT);
    const tiles = grid.locator('.builtin-icon-tile');
    const groups = grid.locator('.builtin-icon-group');
    const lastOfFirst = groups.nth(0).locator('.builtin-icon-tile').last();
    const firstOfSecond = groups.nth(1).locator('.builtin-icon-tile').first();
    const lastTile = tiles.last();
    const none = grid.locator('[data-icon="none"]');

    await lastOfFirst.focus();
    await page.keyboard.press('ArrowRight');
    await expect(firstOfSecond).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(lastOfFirst).toBeFocused();
    await page.keyboard.press('ArrowDown'); // linear: same as Right
    await expect(firstOfSecond).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await expect(lastOfFirst).toBeFocused();
    await page.keyboard.press('Home');
    await expect(none).toBeFocused();
    await page.keyboard.press('End');
    await expect(lastTile).toBeFocused();
    await page.keyboard.press('ArrowRight'); // wraps
    await expect(none).toBeFocused();
    await page.keyboard.press('ArrowLeft'); // wraps back
    await expect(lastTile).toBeFocused();

    // Focus moved, nothing was chosen: "No image" is still the one selected tile.
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(1);
    await expect(none).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#category-icon')).toHaveValue('');
    // Roving: the focused tile holds the tab stop.
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(lastTile).toHaveAttribute('tabindex', '0');
    // A heading is never focus-landed.
    expect(await page.evaluate(() => document.activeElement?.classList.contains('builtin-icon-group-label'))).toBe(false);
    assertClean();
  });

  test('a search filter limits arrows/Home/End to visible tiles', async ({ page }) => {
    await openCategory(page);
    await page.locator(CAT_SEARCH).fill('cake');
    const grid = page.locator(CAT);
    const visible = grid.locator('.builtin-icon-tile:visible');
    const count = await visible.count();
    expect(count).toBeGreaterThan(1);
    expect(count).toBeLessThan(10);
    const want = await visible.evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.icon));
    expect(want[0]).toBe('none');
    await grid.locator('[data-icon="none"]').focus();
    const seen: string[] = ['none'];
    for (let i = 1; i < count; i++) {
      await page.keyboard.press('ArrowRight');
      seen.push(await focusedIcon(page));
    }
    expect(seen).toEqual(want);
    await page.keyboard.press('ArrowRight'); // wraps to the first visible
    expect(await focusedIcon(page)).toBe('none');
    await page.keyboard.press('End');
    expect(await focusedIcon(page)).toBe(want[want.length - 1]);
    await page.keyboard.press('Home');
    expect(await focusedIcon(page)).toBe('none');
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(1);
  });

  test('RTL: ArrowLeft goes to the next tile in DOM order, ArrowRight to the previous', async ({ page }) => {
    await openCategory(page, '?lang=ar');
    expect(await page.evaluate(() => getComputedStyle(document.querySelector('#category-icon-grid')!).direction)).toBe('rtl');
    const tiles = page.locator(`${CAT} .builtin-icon-tile`);
    await tiles.nth(3).focus();
    await page.keyboard.press('ArrowLeft');
    await expect(tiles.nth(4)).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(tiles.nth(3)).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(tiles.nth(2)).toBeFocused();
  });

  test('catalog: arrowing over an item with an uploaded photo writes nothing; Enter chooses', async ({ page }) => {
    const assertClean = watchConsole(page);
    const name = `Listbox5601 Photo ${RUN}`;
    await openItemWithPhoto(page, name);
    // The photo is the item's image: no tile selected.
    const grid = page.locator(ITEM);
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(0);
    const previewBefore = await page.locator('#image-current').getAttribute('src').catch(() => null);

    const posts: string[] = [];
    page.on('request', (r) => {
      if (r.method() === 'POST' && r.url().includes('/api/catalog/item/icon')) posts.push(r.url());
    });
    const tiles = grid.locator('.builtin-icon-tile');
    await grid.locator('[data-icon="none"]').focus();
    for (let i = 0; i < 8; i++) await page.keyboard.press('ArrowRight');
    // Tester (5601): prove the arrows really moved focus, else "no POST" is
    // vacuous (it passed with the listbox branch of radiogroup.js removed).
    expect(await focusedIcon(page)).toBe(await tiles.nth(8).getAttribute('data-icon'));
    await page.keyboard.press('Home');
    await page.keyboard.press('End');
    await page.keyboard.press('Home');
    await page.keyboard.press('ArrowRight');
    await page.waitForTimeout(300);
    expect(posts).toHaveLength(0);
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(0);
    expect(await page.locator('#image-current').getAttribute('src').catch(() => null)).toBe(previewBefore);

    // Enter on the focused tile is the native button click: exactly one write.
    const target = await focusedIcon(page);
    expect(target).toBe(await tiles.nth(1).getAttribute('data-icon')); // Home, ArrowRight
    const resp = page.waitForResponse((r) => r.url().includes('/api/catalog/item/icon') && r.request().method() === 'POST');
    await page.keyboard.press('Enter');
    await resp;
    expect(posts).toHaveLength(1);
    await expect(grid.locator(`[data-icon="${target}"]`)).toHaveAttribute('aria-selected', 'true');
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(1);
    assertClean();
  });

  // Review finding (5601): the catalog repaints selection asynchronously
  // (POST, then GET icon-state). Arrowing on before that repaint lands must
  // not leave the focused tile at tabindex=-1 with the stop on the selected
  // tile, or Shift+Tab from the focused tile lands on that second stop
  // inside the grid instead of leaving it.
  test('catalog: a late selection repaint keeps the stop on the focused tile', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openItemWithPhoto(page, `Listbox5601 Late ${RUN}`);
    const grid = page.locator(ITEM);
    const tiles = grid.locator('.builtin-icon-tile');
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    await page.route('**/api/catalog/item/icon-state*', async (route) => {
      await gate;
      await route.continue();
    });
    await grid.locator('[data-icon="none"]').focus();
    await page.keyboard.press('ArrowRight'); // tiles[1]
    const posted = page.waitForResponse((r) => r.url().includes('/api/catalog/item/icon') && r.request().method() === 'POST');
    await page.keyboard.press('Enter'); // choose tiles[1]; its repaint is held
    await posted;
    for (let i = 0; i < 3; i++) await page.keyboard.press('ArrowRight'); // tiles[4]
    const repainted = page.waitForResponse((r) => r.url().includes('/api/catalog/item/icon-state'));
    release();
    await repainted;
    await expect(tiles.nth(1)).toHaveAttribute('aria-selected', 'true');
    await expect(tiles.nth(4)).toBeFocused();
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(tiles.nth(4)).toHaveAttribute('tabindex', '0');
    await page.keyboard.press('Tab');
    expect(await inGrid(page, ITEM)).toBe(false);
    // Focus left: the stop goes back to the selected tile.
    await expect(tiles.nth(1)).toHaveAttribute('tabindex', '0');
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await page.unroute('**/api/catalog/item/icon-state*');
    assertClean();
  });

  test('category: arrowing then Cancel changes nothing; Space chooses', async ({ page }) => {
    const assertClean = watchConsole(page);
    const name = `Listbox5601 Cat ${RUN}`;
    await openCategory(page);
    await page.locator(NAME).fill(name);
    const grid = page.locator(CAT);
    await grid.locator('[data-icon="coffee"]').click();
    await expect(page.locator('#category-icon')).toHaveValue(/.+/);
    const before = await page.locator('#category-icon').inputValue();
    await page.keyboard.press('ArrowRight');
    for (let i = 0; i < 4; i++) await page.keyboard.press('ArrowRight');
    expect(await page.locator('#category-icon').inputValue()).toBe(before);
    await expect(grid.locator('[data-icon="coffee"]')).toHaveAttribute('aria-selected', 'true');
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(1);

    // Space on the focused (different) tile chooses it.
    const target = await focusedIcon(page);
    expect(target).not.toBe('coffee');
    await page.keyboard.press('Space');
    await expect(grid.locator(`[data-icon="${target}"]`)).toHaveAttribute('aria-selected', 'true');
    expect(await page.locator('#category-icon').inputValue()).not.toBe('');
    await expect(grid.locator('[aria-selected="true"]')).toHaveCount(1);
    assertClean();
  });
});
