import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2506 (first cut): the built-in icon library grew from 5 tiles to
// ~70 in sections, so both pickers gained a search box
// (web/public/icon-picker-filter.js). Pinned here in a real browser, in
// the /categories dialog (the item editor's picker shares the same markup
// and script):
//   (a) the library renders grouped, "No image" first, every tile's image
//       actually loads (a registry key with no tile would 404);
//   (b) search narrows by English keyword and by the visible label, hides
//       empty section headings, shows the no-match line, and never hides
//       "No image";
//   (c) arrow-key navigation skips tiles the search hid;
//   (d) a new icon round-trips: pick "beer", save, reopen → beer pressed;
//   (e) at the 1024x600 floor the grid scrolls inside its own box so the
//       dialog's Save stays reachable.
//
// HONESTY NOTE: Chromium with synthetic input; touch scrolling of the
// inner grid on the pilot tablet is the local hardware lane's to confirm.

const RUN = Date.now().toString(36).toUpperCase();
const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const ICONS = '#category-icon-grid';
const SEARCH = 'input[data-icon-filter="category-icon-grid"]';
const EMPTY = '[data-icon-filter-empty="category-icon-grid"]';

async function openNew(page: Page) {
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
}

test.describe('built-in icon library (ut-docs#2506)', () => {
  test('grouped library, all tiles load, "No image" first', async ({ page }) => {
    const assertNoConsoleErrors = watchConsole(page);
    await openNew(page);
    const tiles = page.locator(`${ICONS} .builtin-icon-tile`);
    expect(await tiles.count()).toBeGreaterThan(60);
    await expect(tiles.first()).toHaveAttribute('data-icon', 'none');
    expect(await page.locator(`${ICONS} .builtin-icon-group-label`).count()).toBe(8);
    // Every tile's <img> resolves (lazy images: scroll each into view).
    const broken: string[] = [];
    const imgs = page.locator(`${ICONS} .builtin-icon-tile img`);
    const n = await imgs.count();
    for (let i = 0; i < n; i++) {
      const img = imgs.nth(i);
      await img.scrollIntoViewIfNeeded();
      await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete)).toBe(true);
      if (await img.evaluate((el: HTMLImageElement) => el.naturalWidth === 0)) {
        broken.push((await img.getAttribute('src')) ?? `#${i}`);
      }
    }
    expect(broken, 'tiles whose image failed to load').toEqual([]);
    assertNoConsoleErrors();
  });

  test('search narrows by keyword and label, keeps "No image"', async ({ page }) => {
    await openNew(page);
    const search = page.locator(SEARCH);

    await search.fill('lager'); // English keyword, not the label
    await expect(page.locator(`${ICONS} [data-icon="beer"]`)).toBeVisible();
    await expect(page.locator(`${ICONS} [data-icon="coffee"]`)).toBeHidden();
    await expect(page.locator(`${ICONS} [data-icon="none"]`)).toBeVisible();
    await expect(page.locator(`${ICONS} .builtin-icon-group-label:visible`)).toHaveCount(1);
    await expect(page.locator(EMPTY)).toBeHidden();

    await search.fill('cake'); // label words: Cake, Slice of cake, Cupcake / muffin
    for (const k of ['cake', 'cake-slice', 'cupcake']) {
      await expect(page.locator(`${ICONS} [data-icon="${k}"]`)).toBeVisible();
    }
    await expect(page.locator(`${ICONS} [data-icon="beer"]`)).toBeHidden();

    await search.fill('zzzqqq');
    await expect(page.locator(EMPTY)).toBeVisible();
    await expect(page.locator(`${ICONS} .builtin-icon-tile:visible`)).toHaveCount(1); // "No image"

    await search.fill('');
    await expect(page.locator(EMPTY)).toBeHidden();
    expect(await page.locator(`${ICONS} .builtin-icon-tile:visible`).count()).toBeGreaterThan(60);
  });

  test('arrow keys skip tiles the search hid', async ({ page }) => {
    await openNew(page);
    await page.locator(SEARCH).fill('wine');
    const wine = page.locator(`${ICONS} [data-icon="wine"]`);
    await wine.click();
    await expect(wine).toHaveAttribute('aria-pressed', 'true');
    await page.keyboard.press('ArrowRight');
    const focused = await page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.icon ?? '');
    expect(['wine-bottle', 'none', 'champagne']).toContain(focused);
    await expect(page.locator(`${ICONS} [data-icon="${focused}"]`)).toBeVisible();
  });

  test('a new icon round-trips through save', async ({ page }) => {
    const name = `Icons2506 ${RUN}`;
    await openNew(page);
    await page.locator(NAME).fill(name);
    await page.locator(SEARCH).fill('beer');
    await page.locator(`${ICONS} [data-icon="beer"]`).click();
    await Promise.all([page.waitForEvent('load'), page.locator(`${DIALOG} .record-dialog-save`).click()]);
    await page.locator('#categories-table .category-row', { hasText: name }).first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${ICONS} [data-icon="beer"]`)).toHaveAttribute('aria-pressed', 'true');
  });

  test('1024x600: the grid scrolls inside its box, Save stays reachable', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await openNew(page);
    const grid = page.locator(ICONS);
    // Bounded by its own max-block-size (18rem; rem varies with the
    // kiosk's root font size), not by the ~70 tiles inside it.
    expect(
      await grid.evaluate((el) => el.getBoundingClientRect().height <= parseFloat(getComputedStyle(el).maxHeight) + 1),
    ).toBe(true);
    expect(await grid.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
    await expect(page.locator(`${DIALOG} .record-dialog-save`)).toBeInViewport();
    await page.locator(SEARCH).scrollIntoViewIfNeeded();
    await page.screenshot({ path: test.info().outputPath('icon-picker-1024x600.png') });
  });

  test('360px phone: tiles fit, no horizontal overflow', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await openNew(page);
    const grid = page.locator(ICONS);
    await grid.scrollIntoViewIfNeeded();
    expect(await grid.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    await page.screenshot({ path: test.info().outputPath('icon-picker-360.png') });
  });

  test('item editor picker: same library and search', async ({ page }) => {
    // Opens an item only to reach the Item image tab; picks nothing, so the
    // shared seed item is left as it was.
    await page.goto('/catalog');
    await page.locator('.catalog-row', { hasText: 'Sparkling Water 500ml' }).click();
    await page.locator('#item-form-tab-image').click();
    const grid = '#builtin-icon-grid';
    expect(await page.locator(`${grid} .builtin-icon-tile`).count()).toBeGreaterThan(60);
    await page.locator('input[data-icon-filter="builtin-icon-grid"]').fill('chai');
    await expect(page.locator(`${grid} [data-icon="tea"]`)).toBeVisible();
    await expect(page.locator(`${grid} [data-icon="beer"]`)).toBeHidden();
    await expect(page.locator(`${grid} [data-icon="none"]`)).toBeVisible();
    await page.screenshot({ path: test.info().outputPath('item-icon-picker.png') });
  });

  test('RTL (fa): grid flows from the inline start, labels translated', async ({ page }) => {
    await page.goto('/categories?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await page.locator('#categories-new').click();
    await expect(page.locator(DIALOG)).toBeVisible();
    const grid = page.locator(ICONS);
    await grid.scrollIntoViewIfNeeded();
    const g = (await grid.boundingBox())!;
    const none = (await page.locator(`${ICONS} [data-icon="none"]`).boundingBox())!;
    expect(none.x + none.width / 2).toBeGreaterThan(g.x + g.width / 2); // first tile on the right
    await expect(page.locator(`${ICONS} [data-icon="tea"] span`)).toHaveText('چای');
    await page.locator(SEARCH).fill('چای');
    await expect(page.locator(`${ICONS} [data-icon="tea"]`)).toBeVisible();
    await expect(page.locator(`${ICONS} [data-icon="beer"]`)).toBeHidden();
    await page.locator(SEARCH).fill('');
    await page.screenshot({ path: test.info().outputPath('icon-picker-fa.png') });
  });

  test('a search does not leak into the next record (review round 1)', async ({ page }) => {
    await openNew(page);
    await page.locator(SEARCH).fill('beer');
    await expect(page.locator(`${ICONS} [data-icon="coffee"]`)).toBeHidden();
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.locator(DIALOG)).toBeHidden();
    await page.locator('#categories-new').click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(SEARCH)).toHaveValue('');
    await expect(page.locator(`${ICONS} [data-icon="coffee"]`)).toBeVisible();
    await expect(page.locator(`${ICONS} .builtin-icon-group-label:visible`)).toHaveCount(8);
  });

  test('Tab from the search box still reaches the filtered grid', async ({ page }) => {
    await openNew(page);
    await page.locator(`${ICONS} [data-icon="beer"]`).click(); // beer holds the tab stop
    await page.locator(SEARCH).fill('cake'); // …and is now hidden
    await page.locator(SEARCH).focus();
    await page.keyboard.press('Tab');
    const focused = await page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.icon ?? '');
    expect(focused).not.toBe('');
  });
});
