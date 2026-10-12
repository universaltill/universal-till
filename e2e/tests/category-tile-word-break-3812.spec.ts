import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { setBrowsingMode } from './helpers';

// ut-docs#3812: on a 360x740 phone the sell screen's category tiles
// (category_tabs mode, three per row) split "Household" as "Househol" /
// "d": a ~88px tile has a ~70px content box, narrower than the word, so
// overflow-wrap had to break inside it. The phone layout now puts two
// category tiles per row. A category name may wrap BETWEEN words (2-line
// clamp), never inside one. Checked in English (the demo catalogue), German (a long
// compound, "Haushaltswaren") and Persian (RTL).
//
// Geometric, not visual: for every word of every visible tile name, a DOM
// Range over that word must lie on one line (its client rects share one
// top). A mid-word split gives the Range two rects on two lines.

const RUN = Date.now().toString(36).toUpperCase();

type Split = { name: string; words: number; split: string[] };

async function splitWords(page: Page, selector: string): Promise<Split[]> {
  return page.locator(selector).evaluateAll((els) =>
    els
      .filter((e) => (e as HTMLElement).offsetParent !== null)
      .map((e) => {
        const text = e.firstChild;
        const split: string[] = [];
        let words = 0;
        if (text && text.nodeType === Node.TEXT_NODE) {
          const data = (text as Text).data;
          const re = /\S+/g;
          let m: RegExpExecArray | null;
          while ((m = re.exec(data))) {
            words++;
            const r = document.createRange();
            r.setStart(text, m.index);
            r.setEnd(text, m.index + m[0].length);
            const tops = new Set(Array.from(r.getClientRects()).map((x) => Math.round(x.top)));
            if (tops.size > 1) split.push(m[0]);
          }
        }
        return { name: e.textContent || '', words, split };
      }),
  );
}

// A category shows as a tile in category_tabs mode only once it has an
// active item (ui.BuildCategoryTiles), so each gets one.
async function createCategory(page: Page, name: string, items: string[]): Promise<void> {
  const resp = await page.request.post('/api/categories', { form: { name }, maxRedirects: 0 });
  expect([200, 303], `create category ${name}`).toContain(resp.status());
  await page.goto('/categories');
  const row = page.locator(`.category-row[data-field-name="${name}"]`);
  await expect(row).toHaveCount(1);
  const catId = (await row.getAttribute('data-id'))!;
  const itemName = `Item3812 ${RUN} ${catId}`;
  const created = await page.request.post('/api/catalog/item', { form: { name: itemName, price: '150', categoryId: catId } });
  expect(created.ok(), `create item ${itemName}`).toBe(true);
  await page.goto('/catalog');
  const itemRow = page.locator(`.catalog-row[data-name="${itemName}"]`);
  await expect(itemRow).toHaveCount(1);
  // Recorded before anything else can throw: a leaked active item keeps
  // its category on the shared worker till's strip for later spec files.
  items.push((await itemRow.first().getAttribute('data-id'))!);
}

test.describe('Category tile names never split mid-word (ut-docs#3812)', () => {
  const items: string[] = [];

  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
    for (const id of items.splice(0)) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  });

  for (const c of [
    { lang: 'en', names: [] as string[] },
    { lang: 'de', names: ['Haushaltswaren', 'Getränke'] },
    { lang: 'fa', names: ['نوشیدنی‌ها', 'لوازم‌خانگی'] },
  ]) {
    test(`360x740, ${c.lang}: every word of every category tile name stays on one line`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 740 });
      for (const n of c.names) await createCategory(page, `${n} ${RUN}`, items);
      await setBrowsingMode(page, 'category_tabs');
      await page.goto(`/?lang=${c.lang}`);
      await expect(page.locator('html')).toHaveAttribute('lang', c.lang);

      const tiles = page.locator('#browsing-category-tiles .category-tile-name');
      await expect(tiles.first()).toBeVisible();
      const names = await tiles.allTextContents();
      expect(names).toContain(c.lang === 'en' ? 'Household' : `${c.names[0]} ${RUN}`);

      const result = await splitWords(page, '#browsing-category-tiles .category-tile-name');
      expect(result.length).toBeGreaterThan(0);
      // Every tile's name was really inspected (a wrapped text node would
      // contribute no words and pass vacuously).
      expect(result.filter((r) => r.words === 0), 'tile names with no inspected words').toEqual([]);
      expect(result.filter((r) => r.split.length), 'category names split mid-word').toEqual([]);

      // Keeping a word whole must not push it past its tile's edge either.
      const overflowing = await page.locator('#browsing-category-tiles .category-tile').evaluateAll((els) =>
        els
          .map((t) => {
            const name = t.querySelector('.category-tile-name')!;
            const tb = t.getBoundingClientRect();
            const nb = name.getBoundingClientRect();
            return { name: name.textContent, inside: nb.left >= tb.left - 0.5 && nb.right <= tb.right + 0.5 && name.scrollWidth <= name.clientWidth + 1 };
          })
          .filter((r) => !r.inside),
      );
      expect(overflowing, 'category names overflowing their tile').toEqual([]);
      // Two per row at this width (three made each tile ~88px — too narrow).
      const cols = await page.locator('#browsing-category-tiles').evaluate(
        (g) => getComputedStyle(g).gridTemplateColumns.split(' ').length,
      );
      expect(cols, 'tiles per row at 360px').toBe(2);

      await page.screenshot({ path: test.info().outputPath(`tiles-${c.lang}.png`) });
    });
  }
});
