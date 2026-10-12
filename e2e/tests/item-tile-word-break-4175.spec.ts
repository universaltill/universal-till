import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { setBrowsingMode, type BrowsingMode } from './helpers';

// ut-docs#4175: on a 360x740 phone the sell screen shows item tiles three
// per row (the approved app-icon mock-up), each ~88px wide. A name at
// .9rem/600 was wider than that for words like "Dishwashing" (94px), so
// overflow-wrap had to break inside them. The phone layout now sets the
// name at TILE_NAME_REM, where every word up to the tile's width fits. A
// word wider than the tile even at that size ("Semi-Skimmed") has to break
// somewhere; a hyphenated one breaks at its hyphen ("Semi-" / "Skimmed"),
// the one acceptable place.
//
// Geometric, not visual: for every unit of every visible tile name, a DOM
// Range over it must lie on one line (its client rects share one top). A
// unit is the whole word if it fits the tile at TILE_NAME_REM, else its
// hyphen-ended segments; a unit that does not fit even then is skipped.
// Measuring at TILE_NAME_REM, not at the rendered size, is what makes this
// fail on the old .9rem: a rule measured at the rendered size would call
// "Dishwashing" unfittable and pass.
//
// Each test creates its own category with its items and opens that
// category, so the tiles under test are really on screen in both browsing
// modes (an uncategorised item is not shown in strip_overflow). The words
// "Dishwashing", "Waschpulver" (de) and "مایع‌ظرفشویی" (fa) are 84-95px at
// .8rem/.9rem: they fit a tile at .8rem and not at .9rem, so each language
// fails on the old size by its own words.

const TILE_NAME_REM = 0.8;
const RUN = Date.now().toString(36).toUpperCase();

type Split = { name: string; units: number; split: string[] };

async function splitUnits(page: Page): Promise<Split[]> {
  return page.locator('.pos-container .products .grid .tile-name').evaluateAll((els, rem) =>
    els
      .filter((e) => (e as HTMLElement).offsetParent !== null)
      .map((e) => {
        const el = e as HTMLElement;
        const text = el.firstChild;
        const split: string[] = [];
        let units = 0;
        if (text && text.nodeType === Node.TEXT_NODE) {
          const tileW = (el.closest('.btn-tile') as HTMLElement).getBoundingClientRect().width;
          const cs = getComputedStyle(el);
          const probe = document.createElement('span');
          const rootPx = parseFloat(getComputedStyle(document.documentElement).fontSize);
          probe.style.cssText = `position:absolute;visibility:hidden;white-space:nowrap;font-family:${cs.fontFamily};font-weight:${cs.fontWeight};letter-spacing:${cs.letterSpacing};font-size:${rem * rootPx}px`;
          document.body.appendChild(probe);
          const width = (s: string) => {
            probe.textContent = s;
            return probe.getBoundingClientRect().width;
          };
          const data = (text as Text).data;
          const re = /\S+/g;
          let m: RegExpExecArray | null;
          while ((m = re.exec(data))) {
            const parts = width(m[0]) <= tileW ? [m[0]] : m[0].split(/(?<=-)/);
            let at = m.index;
            for (const p of parts) {
              if (width(p) <= tileW) {
                units++;
                const r = document.createRange();
                r.setStart(text, at);
                r.setEnd(text, at + p.length);
                const tops = new Set(Array.from(r.getClientRects()).map((x) => Math.round(x.top)));
                if (tops.size > 1) split.push(p);
              }
              at += p.length;
            }
          }
          probe.remove();
        }
        return { name: el.textContent || '', units, split };
      }),
    TILE_NAME_REM,
  );
}

// A category with the given active items. Every item id is recorded before
// anything else can throw: a leaked active item keeps its category on the
// shared worker till's strip for later spec files.
async function createCategory(page: Page, name: string, itemNames: string[], items: string[]): Promise<string> {
  const resp = await page.request.post('/api/categories', { form: { name }, maxRedirects: 0 });
  expect([200, 303], `create category ${name}`).toContain(resp.status());
  await page.goto('/categories');
  const row = page.locator(`.category-row[data-field-name="${name}"]`);
  await expect(row).toHaveCount(1);
  const catId = (await row.getAttribute('data-id'))!;
  for (const itemName of itemNames) {
    const created = await page.request.post('/api/catalog/item', { form: { name: itemName, price: '150', categoryId: catId } });
    expect(created.ok(), `create item ${itemName}`).toBe(true);
    await page.goto('/catalog');
    const itemRow = page.locator(`.catalog-row[data-name="${itemName}"]`);
    await expect(itemRow).toHaveCount(1);
    items.push((await itemRow.first().getAttribute('data-id'))!);
  }
  return catId;
}

test.describe('Item tile names never split mid-word on a phone (ut-docs#4175)', () => {
  const items: string[] = [];

  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
    for (const id of items.splice(0)) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  });

  for (const mode of ['all_filter_chips', 'strip_overflow'] as BrowsingMode[]) {
    for (const c of [
      { lang: 'en', names: ['Dishwashing Liquid', 'Semi-Skimmed Milk'] },
      { lang: 'de', names: ['Waschpulver Color', 'Zahnbürste Reisegröße'] },
      { lang: 'fa', names: ['مایع‌ظرفشویی خانگی', 'شیرینی‌پزی خانگی'] },
    ]) {
      test(`360x740, ${mode}, ${c.lang}: every unit of every item tile name stays on one line`, async ({ page }) => {
        await page.setViewportSize({ width: 360, height: 740 });
        // Unique per test: a deactivated item of an earlier test keeps its
        // name, and the lookups below expect exactly one row.
        const tag = `${RUN}${mode === 'all_filter_chips' ? 'A' : 'S'}`;
        const names = c.names.map((n) => `${n} ${tag}`);
        const catId = await createCategory(page, `Cat4175 ${c.lang} ${tag}`, names, items);
        await setBrowsingMode(page, mode);
        await page.goto(`/?lang=${c.lang}`);
        await expect(page.locator('html')).toHaveAttribute('lang', c.lang);

        // Open the created category: a filter chip in all_filter_chips, the
        // strip tab in strip_overflow (a tab that did not fit the row sits
        // behind the "..." sheet, so the click goes to the tab itself).
        if (mode === 'all_filter_chips') {
          await page.locator(`.pos-container .products .filter-chips .chip[data-cat-id="${catId}"]`).click();
        } else {
          await page.evaluate((id) => (document.getElementById(`cat-tab-${id}`) as HTMLElement).click(), catId);
        }

        // Wait for the created category's own tile (the first tile in the
        // DOM can belong to another, hidden panel).
        await expect(page.locator('.pos-container .products .grid .tile-name', { hasText: names[0] })).toBeVisible();

        const result = await splitUnits(page);
        // The tiles under test were really measured (visible), not just
        // present in the DOM: a hidden tile would pass vacuously.
        for (const n of names) expect(result.map((r) => r.name), `measured tile for ${n}`).toContain(n);
        expect(result.filter((r) => r.units === 0 && r.name.trim() !== ''), 'tile names with no inspected unit').toEqual([]);
        expect(result.filter((r) => r.split.length), 'item names split mid-word').toEqual([]);

        // Three per row, as in the approved mock-up (this card keeps it).
        const cols = await page
          .locator('.pos-container .products .grid:visible')
          .first()
          .evaluate((g) => getComputedStyle(g).gridTemplateColumns.split(' ').length);
        expect(cols, 'tiles per row at 360px').toBe(3);

        // Keeping a word whole must not push it past its tile's edge.
        const overflowing = await page.locator('.pos-container .products .grid .btn-tile').evaluateAll((els) =>
          els
            .filter((t) => (t as HTMLElement).offsetParent !== null)
            .map((t) => {
              const name = t.querySelector('.tile-name');
              if (!name) return null;
              const tb = t.getBoundingClientRect();
              const nb = name.getBoundingClientRect();
              return { name: name.textContent, inside: nb.left >= tb.left - 0.5 && nb.right <= tb.right + 0.5 };
            })
            .filter((r) => r && !r.inside),
        );
        expect(overflowing, 'item names overflowing their tile').toEqual([]);

        await page.screenshot({ path: test.info().outputPath(`tiles-${mode}-${c.lang}.png`) });
      });
    }
  }
});
