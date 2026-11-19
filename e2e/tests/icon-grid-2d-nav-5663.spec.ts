import path from 'path';
import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5663 (follow-up to #5601): in the built-in icon listboxes
// (web/public/radiogroup.js, data-roving-listbox) Up/Down move by VISUAL
// row (same column, crossing sections), PageUp/PageDown jump by section,
// and typing the start of a name jumps to it (750 ms idle window). Still
// focus-only: none of it chooses an icon or POSTs.
//
// The oracle below reads geometry in-page with different code from the
// implementation (per-group offsetTop rows + rect centres).
// HONESTY NOTE: Chromium, synthetic keys; no screen reader, no touch HW.

const RUN = Date.now().toString(36).toUpperCase();
const DIALOG = '#category-dialog';
const CAT = '#category-icon-grid';
const ITEM = '#builtin-icon-grid';
const CAT_SEARCH = 'input[data-icon-filter="category-icon-grid"]';
const ICON_PNG = path.join(__dirname, '../fixtures/test-item-image.png');
const IDLE = 1000; // > 750 ms type-ahead window + margin

async function openCategory(page: Page, query = '') {
  await page.goto(`/categories${query}`);
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
}

async function openItemWithPhoto(page: Page, name: string) {
  const created = await page.request.post('/api/catalog/item', { form: { name, price: '199' } });
  expect(created.ok()).toBeTruthy();
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: name }).first().click();
  await page.locator('#item-form-tab-image').click();
  await page.locator('#image-file').setInputFiles(ICON_PNG);
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');
}

type Tile = { icon: string; cx: number; top: number; group: number; row: number; col: number; text: string };
// Oracle: every visible option with its section and visual row (rows grouped
// by offsetTop within the group), column and centre-x.
async function layout(page: Page, grid: string): Promise<Tile[]> {
  return page.evaluate((sel) => {
    const box = document.querySelector(sel)!;
    const out: any[] = [];
    const opts = Array.from(box.querySelectorAll<HTMLElement>('[role="option"]')).filter((o) => !o.closest('[hidden]') && !o.hidden);
    const groups = new Map<Element | null, HTMLElement[]>();
    let gi = -1;
    const gidx = new Map<Element | null, number>();
    for (const o of opts) {
      const g = o.closest('.builtin-icon-group');
      if (!gidx.has(g)) { gidx.set(g, ++gi); groups.set(g, []); }
      groups.get(g)!.push(o);
    }
    for (const [g, list] of groups) {
      const tops = Array.from(new Set(list.map((o) => o.offsetTop))).sort((a, b) => a - b);
      const colCount: Record<number, number> = {};
      for (const o of list) {
        const r = o.getBoundingClientRect();
        const row = tops.indexOf(o.offsetTop);
        const col = (colCount[row] = (colCount[row] ?? -1) + 1);
        out.push({
          icon: o.dataset.icon ?? '', cx: (r.left + r.right) / 2, top: r.top,
          group: gidx.get(g), row, col, text: (o.textContent ?? '').replace(/\s+/g, ' ').trim(),
        });
      }
    }
    return out;
  }, grid);
}
const focusedIcon = (page: Page) =>
  page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.icon ?? '');
const focus = (page: Page, grid: string, icon: string) => page.locator(`${grid} [data-icon="${icon}"]`).focus();
const stops = (page: Page, grid: string) => expect(page.locator(`${grid} [tabindex="0"]`)).toHaveCount(1);
const norm = (s: string) => s.toLocaleLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '');

function sections(t: Tile[]) {
  const m = new Map<number, Tile[][]>();
  for (const x of t) {
    const rows = m.get(x.group) ?? [];
    (rows[x.row] ??= []).push(x);
    m.set(x.group, rows);
  }
  return [...m.keys()].sort((a, b) => a - b).map((k) => m.get(k)!);
}
// Index (>=1) of the first real section that has >=2 visual rows and a
// following section, so the test never hard-codes a column count.
const multiRow = (secs: Tile[][][]) => {
  const k = secs.findIndex((rows, i) => i >= 1 && rows.length >= 2 && i + 1 < secs.length);
  expect(k, 'a multi-row section followed by another section').toBeGreaterThan(0);
  return k;
};
const nearest = (row: Tile[], cx: number) =>
  row.reduce((b, t) => (Math.abs(t.cx - cx) < Math.abs(b.cx - cx) ? t : b));

for (const which of ['category', 'catalog'] as const) {
  test.describe(`icon grid 2-D keys: ${which} (ut-docs#5663)`, () => {
    let grid: string;
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize({ width: 1024, height: 600 });
      if (which === 'category') { grid = CAT; await openCategory(page); }
      else { grid = ITEM; await openItemWithPhoto(page, `Grid5663 ${which} ${RUN} ${test.info().workerIndex}`); }
    });

    test('Down/Up keep the column inside a section and cross sections by nearest centre', async ({ page }) => {
      const assertClean = watchConsole(page);
      const secs = sections(await layout(page, grid));
      const k = multiRow(secs);
      const s0 = secs[k];
      // AC1: mid-row tile of group 0 -> same column next row.
      const from = s0[0].find((a) => s0[1].some((b) => Math.abs(b.cx - a.cx) < 2))!;
      const below = s0[1].find((t) => Math.abs(t.cx - from.cx) < 2)!;
      expect(below).toBeTruthy();
      await focus(page, grid, from.icon);
      await page.keyboard.press('ArrowDown');
      expect(await focusedIcon(page)).toBe(below.icon);
      await stops(page, grid);
      await page.keyboard.press('ArrowUp');
      expect(await focusedIcon(page)).toBe(from.icon);
      // AC2: last row of group 0 -> first row of group 1, nearest centre.
      const last = s0[s0.length - 1][0];
      const next = nearest(secs[k + 1][0], last.cx);
      await focus(page, grid, last.icon);
      await page.keyboard.press('ArrowDown');
      expect(await focusedIcon(page)).toBe(next.icon);
      // AC3: Up from there -> last row of group 0 nearest centre.
      await page.keyboard.press('ArrowUp');
      expect(await focusedIcon(page)).toBe(nearest(s0[s0.length - 1], next.cx).icon);
      await stops(page, grid);
      assertClean();
    });

    test('short last row: Down lands on the last tile of the shorter row', async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 640 });
      const secs = sections(await layout(page, grid)).slice(1);
      let found: [Tile, Tile[]] | null = null;
      for (const rows of secs) {
        for (let r = 0; r + 1 < rows.length && !found; r++) {
          const a = rows[r], b = rows[r + 1];
          if (b.length < a.length) found = [a[a.length - 1], b];
        }
      }
      test.skip(!found, 'no section with a short last row at this width');
      const [from, nextRow] = found!;
      await focus(page, grid, from.icon);
      await page.keyboard.press('ArrowDown');
      expect(await focusedIcon(page)).toBe(nearest(nextRow, from.cx).icon);
      expect(await focusedIcon(page)).toBe(nextRow[nextRow.length - 1].icon);
    });

    test('Up/Down wrap, with "No image" as the first row', async ({ page }) => {
      const secs = sections(await layout(page, grid));
      const lastSec = secs[secs.length - 1];
      await page.keyboard.press('End'); // no-op if focus elsewhere; focus first
      await focus(page, grid, 'none');
      await page.keyboard.press('End');
      const endIcon = await focusedIcon(page);
      expect(endIcon).toBe(lastSec[lastSec.length - 1][lastSec[lastSec.length - 1].length - 1].icon);
      await page.keyboard.press('ArrowDown'); // last row of last section -> wraps
      expect(await focusedIcon(page)).toBe('none');
      const none = secs[0][0][0];
      await page.keyboard.press('ArrowUp');
      expect(await focusedIcon(page)).toBe(nearest(lastSec[lastSec.length - 1], none.cx).icon);
      await focus(page, grid, 'none');
      await page.keyboard.press('ArrowDown');
      expect(await focusedIcon(page)).toBe(nearest(secs[1][0], none.cx).icon);
    });

    test('PageDown/PageUp jump by section, no wrap, headings never focused', async ({ page }) => {
      const assertClean = watchConsole(page);
      const secs = sections(await layout(page, grid));
      const firsts = secs.map((s) => s[0][0].icon);
      await focus(page, grid, 'none');
      await page.keyboard.press('PageUp');
      expect(await focusedIcon(page)).toBe('none');
      for (let i = 1; i < firsts.length; i++) {
        await page.keyboard.press('PageDown');
        expect(await focusedIcon(page)).toBe(firsts[i]);
        await expect(page.locator(`${grid} [data-icon="${firsts[i]}"]`)).toBeInViewport();
        await stops(page, grid);
      }
      await page.keyboard.press('PageDown'); // last section: no-op
      expect(await focusedIcon(page)).toBe(firsts[firsts.length - 1]);
      // Mid-section: PageUp goes to the start of the current section first.
      const second = secs[2][0][1].icon;
      await focus(page, grid, second);
      await page.keyboard.press('PageUp');
      expect(await focusedIcon(page)).toBe(firsts[2]);
      await page.keyboard.press('PageUp');
      expect(await focusedIcon(page)).toBe(firsts[1]);
      await page.keyboard.press('PageUp');
      expect(await focusedIcon(page)).toBe('none');
      expect(await page.evaluate(() => document.activeElement?.classList.contains('builtin-icon-group-label'))).toBe(false);
      assertClean();
    });

    test('a filter limits Down/PageDown/type-ahead to visible tiles', async ({ page }) => {
      if (which === 'catalog') await page.locator('input[data-icon-filter="builtin-icon-grid"]').fill('cake');
      else await page.locator(CAT_SEARCH).fill('cake');
      const t = await layout(page, grid);
      expect(t.length).toBeGreaterThan(1);
      expect(t.length).toBeLessThan(15);
      const visible = new Set(t.map((x) => x.icon));
      await focus(page, grid, 'none');
      for (let i = 0; i < t.length + 2; i++) {
        await page.keyboard.press('ArrowDown');
        expect(visible.has(await focusedIcon(page))).toBe(true);
      }
      await page.keyboard.press('PageDown');
      expect(visible.has(await focusedIcon(page))).toBe(true);
      await page.keyboard.type('c');
      expect(visible.has(await focusedIcon(page))).toBe(true);
    });

    test('type-ahead: prefix, repeat-letter cycling, no match, Space in buffer, one tab stop', async ({ page }) => {
      const assertClean = watchConsole(page);
      const t = await layout(page, grid);
      const names = t.filter((x) => x.icon !== 'none');
      const first = (p: string) => names.find((x) => norm(x.text).startsWith(p));
      await focus(page, grid, 'none');
      const be = first('be')!;
      expect(be).toBeTruthy();
      await page.keyboard.type('be');
      expect(await focusedIcon(page)).toBe(be.icon);
      await stops(page, grid);
      await page.waitForTimeout(IDLE);
      // Repeated letter cycles through tiles starting with it.
      const bs = names.filter((x) => norm(x.text).startsWith('b'));
      expect(bs.length).toBeGreaterThan(1);
      await focus(page, grid, 'none');
      await page.keyboard.press('b');
      expect(await focusedIcon(page)).toBe(bs[0].icon);
      await page.keyboard.press('b');
      expect(await focusedIcon(page)).toBe(bs[1].icon);
      await page.waitForTimeout(IDLE);
      // No match: focus unchanged.
      const before = await focusedIcon(page);
      await page.keyboard.type('xqz');
      expect(await focusedIcon(page)).toBe(before);
      await page.waitForTimeout(IDLE);
      // Space inside a buffer is a character and does not choose.
      const ice = names.find((x) => norm(x.text) === 'ice cream');
      if (ice) {
        const sel = await page.locator(`${grid} [aria-selected="true"]`).evaluateAll((e) => e.map((x) => (x as HTMLElement).dataset.icon));
        await focus(page, grid, 'none');
        await page.keyboard.type('ice');
        await page.keyboard.press('Space');
        await page.keyboard.press('c');
        expect(await focusedIcon(page)).toBe(ice.icon);
        await page.waitForTimeout(200);
        const sel2 = await page.locator(`${grid} [aria-selected="true"]`).evaluateAll((e) => e.map((x) => (x as HTMLElement).dataset.icon));
        expect(sel2).toEqual(sel);
      }
      assertClean();
    });

    test('type-ahead survives a lone Shift / dead-key keydown mid-word (review F1)', async ({ page }) => {
      const names = (await layout(page, grid)).filter((x) => x.icon !== 'none');
      const ice = names.find((x) => norm(x.text) === 'ice cream');
      const be = names.find((x) => norm(x.text).startsWith('be'));
      await focus(page, grid, 'none');
      if (ice) {
        await page.keyboard.type('ice');
        await page.keyboard.press('Space');
        await page.keyboard.down('Shift');
        await page.keyboard.press('C');
        await page.keyboard.up('Shift');
        expect(await focusedIcon(page)).toBe(ice.icon);
        await page.waitForTimeout(IDLE);
        await focus(page, grid, 'none');
        await page.keyboard.type('ice');
        await page.keyboard.press('Space');
        await page.evaluate(() => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Dead', bubbles: true })));
        await page.keyboard.press('c');
        expect(await focusedIcon(page)).toBe(ice.icon);
        await page.waitForTimeout(IDLE);
      }
      // Capital first letter (Shift down) then a second letter.
      await focus(page, grid, 'none');
      await page.keyboard.down('Shift');
      await page.keyboard.press('B');
      await page.keyboard.up('Shift');
      await page.keyboard.press('e');
      expect(be).toBeTruthy();
      expect(await focusedIcon(page)).toBe(be!.icon);
    });

    test('Ctrl/Alt/Meta + letter does not move focus; Space after the idle window chooses (AC7, AC9)', async ({ page }) => {
      await focus(page, grid, 'none');
      for (const k of ['Control+b', 'Alt+b', 'Meta+b']) {
        await page.keyboard.press(k);
        expect(await focusedIcon(page)).toBe('none');
      }
      await page.keyboard.type('be');
      const at = await focusedIcon(page);
      expect(at).not.toBe('none');
      await page.waitForTimeout(IDLE);
      await page.keyboard.press('Space');
      await expect(page.locator(`${grid} [data-icon="${at}"]`)).toHaveAttribute('aria-selected', 'true');
    });

    test('Up/Down/PageUp/PageDown/type-ahead never choose or POST; Enter then does once', async ({ page }) => {
      const posts: string[] = [];
      page.on('request', (r) => {
        if (r.method() === 'POST' && /\/api\/catalog\/item\/icon|\/api\/categor/.test(r.url())) posts.push(r.url());
      });
      const selBefore = await page.locator(`${grid} [aria-selected="true"]`).count();
      await focus(page, grid, 'none');
      const seen = new Set<string>(['none']);
      for (const k of ['ArrowDown', 'ArrowDown', 'ArrowUp', 'PageDown', 'PageUp', 'PageDown']) {
        await page.keyboard.press(k);
        seen.add(await focusedIcon(page));
      }
      await page.waitForTimeout(IDLE);
      await page.keyboard.type('be');
      await page.waitForTimeout(IDLE);
      await page.keyboard.type('ice');
      await page.keyboard.press('Space');
      await page.keyboard.press('c');
      seen.add(await focusedIcon(page));
      expect(seen.size).toBeGreaterThan(3); // focus really moved: "no POST" is not vacuous
      await page.waitForTimeout(300);
      expect(posts).toHaveLength(0);
      expect(await page.locator(`${grid} [aria-selected="true"]`).count()).toBe(selBefore);
      if (which === 'catalog') {
        await expect(page.locator(`${ITEM} [aria-selected="true"]`)).toHaveCount(0);
        const resp = page.waitForResponse((r) => r.url().includes('/api/catalog/item/icon') && r.request().method() === 'POST');
        await page.keyboard.press('Enter');
        await resp;
        expect(posts).toHaveLength(1);
      }
    });
  });
}

test('RTL: Up/Down keep the visual column, PageDown -> next section, ArrowLeft stays DOM-next', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await openCategory(page, '?lang=ar');
  expect(await page.evaluate(() => getComputedStyle(document.querySelector('#category-icon-grid')!).direction)).toBe('rtl');
  const secs = sections(await layout(page, CAT));
  const k = multiRow(secs);
  const s0 = secs[k];
  const from = s0[0].find((a) => s0[1].some((b) => Math.abs(b.cx - a.cx) < 2))!;
  await focus(page, CAT, from.icon);
  await page.keyboard.press('ArrowDown');
  const landed = nearest(s0[1], from.cx);
  expect(await focusedIcon(page)).toBe(landed.icon);
  expect(Math.abs(landed.cx - from.cx)).toBeLessThan(2);
  expect(landed.top).toBeGreaterThan(from.top);
  await focus(page, CAT, from.icon);
  await page.keyboard.press('PageDown');
  expect(await focusedIcon(page)).toBe(secs[k + 1][0][0].icon);
  const all = page.locator(`${CAT} .builtin-icon-tile`);
  await all.nth(3).focus();
  await page.keyboard.press('ArrowLeft');
  await expect(all.nth(4)).toBeFocused();
});
