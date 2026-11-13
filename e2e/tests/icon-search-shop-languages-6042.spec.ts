import fs from 'fs';
import path from 'path';
import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#6042: the icon pickers (item editor + category dialog) find tiles
// by their names in the SHOP's languages, with a language-neutral fold on
// both sides (web/public/icon-picker-filter.js `fold` = internal/catimport
// FoldForSearch). The e2e till's default language is en, so tr/ar become shop
// languages through Settings → Languages shown to staff. True tr-/ar-default
// shops are covered by the Go render tests.
//
// HONESTY NOTE: Chromium with synthetic input and an en-US browser locale
// (deliberately not Turkish: the fold must not depend on it).

const RUN = Date.now().toString(36).toUpperCase();
const CAT = '#category-icon-grid';
const ITEM = '#builtin-icon-grid';
const CAT_SEARCH = 'input[data-icon-filter="category-icon-grid"]';
const ITEM_SEARCH = 'input[data-icon-filter="builtin-icon-grid"]';
const CAT_EMPTY = '[data-icon-filter-empty="category-icon-grid"]';

async function setStaff(page: Page, langs: string[]) {
  const resp = await page.request.post('/api/settings/staff-languages', {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    data: langs.map((l) => `staff_locales=${l}`).join('&'),
  });
  expect(resp.status()).toBe(204);
}

async function openCategory(page: Page, lang: string) {
  await page.goto(`/categories?lang=${lang}`);
  await page.locator('#categories-new').click();
  await expect(page.locator('#category-dialog')).toBeVisible();
  return { grid: page.locator(CAT), search: page.locator(CAT_SEARCH) };
}

async function openItem(page: Page, lang: string) {
  const name = `Icon search ${RUN} ${lang}`;
  const created = await page.request.post('/api/catalog/item', { form: { name, price: '199' } });
  expect(created.ok()).toBeTruthy();
  await page.goto(`/catalog?lang=${lang}`);
  await page.locator('.catalog-row', { hasText: name }).first().click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator(ITEM)).toBeVisible();
  return { grid: page.locator(ITEM), search: page.locator(ITEM_SEARCH) };
}

const pickers = [
  ['category dialog', openCategory],
  ['item editor', openItem],
] as const;

test.describe('icon search in the shop languages (ut-docs#6042)', () => {
  test.afterEach(async ({ page }) => {
    await setStaff(page, ['en']);
  });

  test('JS fold matches the Go fold on the shared fixture (real Chromium)', async ({ page }) => {
    const rows: { in: string; out: string }[] = JSON.parse(
      fs.readFileSync(path.join(__dirname, '../../internal/catimport/testdata/search_fold_parity.json'), 'utf8'),
    );
    await openCategory(page, 'en');
    const got = await page.evaluate(
      (rs) => rs.map((r) => (window as unknown as { utIconFold: (s: string) => string }).utIconFold(r.in)),
      rows,
    );
    expect(got).toEqual(rows.map((r) => r.out));
  });

  for (const [label, open] of pickers) {
    test(`${label}: en+tr shop, UI English: "kahve" and "KAHVE" find coffee; "lager" still finds beer`, async ({ page }) => {
      await setStaff(page, ['en', 'tr']);
      const { grid, search } = await open(page, 'en');
      for (const q of ['kahve', 'KAHVE']) {
        await search.fill(q);
        await expect(grid.locator('[data-icon="coffee"]')).toBeVisible();
        await expect(grid.locator('[data-icon="beer"]')).toBeHidden();
      }
      await search.fill('lager');
      await expect(grid.locator('[data-icon="beer"]')).toBeVisible();
      // Dotless ı typed on an English UI: only the server's folded keywords
      // (and the JS fold of the query) can match — the visible label is English.
      await search.fill('balık');
      await expect(grid.locator('[data-icon="fish"]')).toBeVisible();
    });

    test(`${label}: UI Turkish: dotted/dotless i fold in any case`, async ({ page }) => {
      await setStaff(page, ['en', 'tr']);
      const { grid, search } = await open(page, 'tr');
      for (const q of ['BALIK', 'balık', 'balik']) {
        await search.fill(q);
        await expect(grid.locator('[data-icon="fish"]')).toBeVisible();
      }
      for (const q of ['İÇECEK', 'icecek']) {
        await search.fill(q);
        await expect(grid.locator('[data-icon="drink"]')).toBeVisible();
      }
    });

    for (const lang of ['ar', 'en']) {
      test(`${label}: en+ar shop, UI ${lang}: Arabic spellings find coffee`, async ({ page }) => {
        await setStaff(page, ['en', 'ar']);
        const { grid, search } = await open(page, lang);
        for (const q of ['قهوه', 'قهوة', 'قَهْوَة', 'القهوة']) {
          await search.fill(q);
          await expect(grid.locator('[data-icon="coffee"]'), q).toBeVisible();
        }
      });
    }
    test(`${label}: en+fa shop, UI en: ZWNJ is not a word boundary (ut-docs#6052)`, async ({ page }) => {
      await setStaff(page, ['en', 'fa']);
      const { grid, search } = await open(page, 'en');
      for (const q of ['هاتداگ', 'هات\u200cداگ', 'هات داگ']) {
        await search.fill(q);
        await expect(grid.locator('[data-icon="hot-dog"]'), q).toBeVisible();
      }
    });
  }

  test('English-only shop: "kahve" finds nothing, "No image" stays', async ({ page }) => {
    await setStaff(page, ['en']);
    const { grid, search } = await openCategory(page, 'en');
    await search.fill('kahve');
    await expect(page.locator(CAT_EMPTY)).toBeVisible();
    await expect(grid.locator('[data-icon="none"]')).toBeVisible();
    await expect(grid.locator('.builtin-icon-tile:visible')).toHaveCount(1);
  });
});
