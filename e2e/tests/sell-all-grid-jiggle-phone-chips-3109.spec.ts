import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#3109: on a phone (<=480px) the all_filter_chips sale screen has a
// sticky category chip row (top: -.7rem), and edit mode adds the sticky Done
// bar (z-index 3). Both stuck at the top of the scrolling .products, so the
// taller bar painted over the chips once the list scrolled -- the chips could
// only be reached by scrolling back to the top -- and the long title wrapped
// to 3-4 lines. The fix gives the bar a fixed height with a short phone title
// and sticks the chips directly below it.
//
// HONESTY NOTE: Playwright's synthetic mouse pointer in Chromium, not a
// finger on the pilot till's WebKitGTK kiosk (same as sell-tile-jiggle-mode-2339).

const SHOTS = process.env.SHOTS_DIR || '/tmp/claude-0/3109/shots';
const RUN = Date.now().toString(36).toUpperCase();
type Item = { name: string; sku: string; category: string };
const CATS = [0, 1, 2, 3].map((i) => `Chips3109 C${i} ${RUN}`);
const ITEMS: Item[] = CATS.flatMap((cat, c) =>
  Array.from({ length: 10 }, (_, n) => ({ name: `Chips3109 I${c}x${n} ${RUN}`, sku: `CH3109${c}${n}${RUN}`, category: cat })),
);

// The de pack string for buttons.edit_mode.title_all_grid_compact. de ships as
// a language-pack plugin, so ?lang=de falls back to en on this till
// (form-label-layout-300.spec.ts); substitute the real pack text instead.
// (The design's first de wording, "Artikel bearbeiten — Kategorie zum Sortieren
// wählen", wrapped to 3 lines at 360px -- the title column is only ~198px --
// so the de pack gets the shorter text below. es/pt fit as designed.)
const PACK_COMPACT: Record<string, string> = {
  de: 'Artikel — Kategorie zum Sortieren wählen',
  es: 'Editando artículos — elige una categoría para ordenar',
  pt: 'Editando itens — escolha uma categoria para ordenar',
};
const EN_FULL = 'Editing items — choose a category to drag its items into order; the pencil edits an item';
const EN_COMPACT = 'Editing items — pick a category to reorder';
const EN_QUICK = 'Editing quick buttons — drag a tile to reorder';

async function seed(page: Page) {
  const csv = 'Name,SKU,Barcode,Price,Category,In stock\n' + ITEMS.map((i) => `${i.name},${i.sku},,1.00,${i.category},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-3109-${RUN}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
}
async function cleanup(page: Page) {
  await page.goto('/catalog');
  for (const it of ITEMS) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}
async function longPress(tile: Locator) {
  await tile.scrollIntoViewIfNeeded();
  const page = tile.page();
  const b = (await tile.boundingBox())!;
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}
async function arm(page: Page) {
  await longPress(page.locator('#buttons-grid-all .btn-tile').first());
  await expect(page.locator('#buttons-grid')).toHaveClass(/jiggle-mode/);
  await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
}
const scrollProducts = (page: Page, where: 'top' | 'mid' | 'max') =>
  page.evaluate((w) => {
    const el = document.querySelector('.pos-container > .products') as HTMLElement;
    el.scrollTop = w === 'top' ? 0 : w === 'max' ? el.scrollHeight : Math.floor((el.scrollHeight - el.clientHeight) / 2);
    return { top: el.scrollTop, max: el.scrollHeight - el.clientHeight };
  }, where);

// Every chip (scrolled into view sideways) must be what is hit at its centre,
// not the Done bar; and the row must be tall enough to tap and on screen.
async function chipsReachable(page: Page, label: string) {
  const res = await page.evaluate(() => {
    const row = document.querySelector('#browsing-category-chips') as HTMLElement;
    const bar = document.querySelector('.jiggle-bar') as HTMLElement;
    const out: { id: string; hit: string; covered: boolean; h: number }[] = [];
    for (const chip of Array.from(row.querySelectorAll('.chip')) as HTMLElement[]) {
      chip.scrollIntoView({ inline: 'nearest', block: 'nearest' });
      const r = chip.getBoundingClientRect();
      const rr = row.getBoundingClientRect();
      const x = (Math.max(r.left, rr.left) + Math.min(r.right, rr.right)) / 2;
      const y = r.top + r.height / 2;
      const hit = document.elementFromPoint(x, y);
      out.push({
        id: chip.dataset.catId || chip.textContent!.trim(),
        hit: hit ? (hit as HTMLElement).className || hit.tagName : 'null',
        covered: !hit || !(chip === hit || chip.contains(hit)) || bar.contains(hit),
        h: r.height,
      });
    }
    const rr = row.getBoundingClientRect();
    return { chips: out, rowBottom: rr.bottom, vh: window.innerHeight, barBottom: bar.getBoundingClientRect().bottom, rowTop: rr.top };
  });
  expect(res.chips.length, `${label}: chips rendered`).toBeGreaterThanOrEqual(4);
  for (const c of res.chips) {
    expect(c.covered, `${label}: chip ${c.id} hit element was ${c.hit}`).toBe(false);
    expect(c.h, `${label}: chip ${c.id} block size`).toBeGreaterThanOrEqual(40);
  }
  expect(res.rowBottom, `${label}: chip row on screen`).toBeLessThanOrEqual(res.vh);
  expect(res.rowTop, `${label}: chip row starts at/below the bar's bottom`).toBeGreaterThanOrEqual(res.barBottom - 1);
}

async function titleFitsTwoLines(page: Page, label: string) {
  const m = await page.evaluate(() => {
    const t = document.querySelector('.jiggle-bar-title') as HTMLElement;
    const cs = getComputedStyle(t);
    const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
    return { h: t.getBoundingClientRect().height, lh, sh: t.scrollHeight, ch: t.clientHeight, text: t.innerText };
  });
  expect(m.h, `${label}: title <= 2 lines (h=${m.h}, lh=${m.lh}, "${m.text}")`).toBeLessThanOrEqual(2 * m.lh + 1);
  expect(m.sh, `${label}: nothing clipped`).toBeLessThanOrEqual(m.ch + 1);
}

test.describe('Phone edit-mode Done bar vs category chips (ut-docs#3109)', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await seed(page);
    await page.close();
  });
  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    await cleanup(page);
    await setBrowsingMode(page, 'strip_overflow');
    await page.close();
  });
  test.beforeEach(async ({ page }) => {
    await setBrowsingMode(page, 'all_filter_chips');
  });
  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
    await page.goto('/?lang=en');
  });

  for (const lang of ['en', 'fa']) {
    test(`360x740 ${lang}: chips stay reachable and Done stays on top while scrolling in edit mode`, async ({ page }, testInfo) => {
      test.setTimeout(60_000);
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto(`/?lang=${lang}`);
      await expect(page.locator('html')).toHaveAttribute('lang', lang);
      if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      await expect(page.locator('#buttons-grid-all .btn-tile').first()).toBeVisible();
      await arm(page);

      const s = await scrollProducts(page, 'max');
      expect(s.max, 'the list scrolls (>= 1 screen of overflow)').toBeGreaterThan(300);
      for (const where of ['top', 'mid', 'max'] as const) {
        await scrollProducts(page, where);
        await chipsReachable(page, `${lang} scroll=${where}`);
      }
      // Screenshot mid-scroll for the review record.
      await scrollProducts(page, 'mid');
      await chipsReachable(page, `${lang} mid again`);
      await page.screenshot({ path: `${SHOTS}/360x740-${lang}-edit-mid.png` });
      await testInfo.attach(`360x740-${lang}-edit-mid`, { path: `${SHOTS}/360x740-${lang}-edit-mid.png`, contentType: 'image/png' });

      // Done is on top and hit at its centre at max scroll.
      await scrollProducts(page, 'max');
      const doneHit = await page.evaluate(() => {
        const d = document.querySelector('[data-testid="jiggle-done"]') as HTMLElement;
        const r = d.getBoundingClientRect();
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return { ok: hit === d || d.contains(hit), inView: r.top >= 0 && r.bottom <= window.innerHeight };
      });
      expect(doneHit.inView, 'Done in viewport at max scroll').toBe(true);
      expect(doneHit.ok, 'Done is the hit element at its centre').toBe(true);

      await titleFitsTwoLines(page, lang);

      // A real tap on a non-All chip at max scroll swaps the grid. Tester
      // (ut-docs#3109): a pointer press at the chip's centre, NOT
      // locator.click() -- click()'s actionability retries scroll .products
      // back until the chip is uncovered, so it passed against main's CSS
      // (verified) and never tapped at max scroll at all.
      const chip = page.locator('#browsing-category-chips .chip[data-cat-id]:not([data-cat-id="all"])').first();
      const pt = await chip.evaluate((c) => {
        c.scrollIntoView({ inline: 'nearest', block: 'nearest' });
        const r = c.getBoundingClientRect();
        const rr = c.parentElement!.getBoundingClientRect();
        const el = document.querySelector('.pos-container > .products') as HTMLElement;
        return { x: (Math.max(r.left, rr.left) + Math.min(r.right, rr.right)) / 2, y: r.top + r.height / 2, st: el.scrollTop, max: el.scrollHeight - el.clientHeight };
      });
      expect(pt.st, 'still at max scroll when tapping the chip').toBeGreaterThanOrEqual(pt.max - 1);
      const [req] = await Promise.all([
        page.waitForRequest((r) => r.url().includes('/ui/buttons/all/more') && /category=(?!all)/.test(r.url()), { timeout: 5_000 }),
        page.mouse.click(pt.x, pt.y),
      ]);
      expect(req.url()).toContain('category=');
      await expect(chip).toHaveAttribute('aria-pressed', 'true');
      assertClean();
    });
  }

  test('360x740 de/es/pt (pack strings injected): title still fits two lines beside Done', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/');
    await expect(page.locator('#buttons-grid-all .btn-tile').first()).toBeVisible();
    await arm(page);
    await scrollProducts(page, 'mid');
    for (const [lang, txt] of Object.entries(PACK_COMPACT)) {
      const applied = await page.evaluate((t) => {
        const el = document.querySelector('.jiggle-bar-title-compact');
        if (!el) return false;
        el.textContent = t;
        return true;
      }, txt);
      expect(applied, 'compact title span exists').toBe(true);
      await titleFitsTwoLines(page, lang);
      await chipsReachable(page, lang);
      if (lang === 'de') {
        await page.screenshot({ path: `${SHOTS}/360x740-de-injected-edit-mid.png` });
        await testInfo.attach('360x740-de-injected-edit-mid', { path: `${SHOTS}/360x740-de-injected-edit-mid.png`, contentType: 'image/png' });
      }
    }
  });

  test('1024x600 en: bar and chip row keep the pre-fix desktop behaviour and full title', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await expect(page.locator('#buttons-grid-all .btn-tile').first()).toBeVisible();
    await arm(page);
    const m = await page.evaluate(() => {
      const bar = document.querySelector('.jiggle-bar') as HTMLElement;
      const row = document.querySelector('#browsing-category-chips') as HTMLElement;
      const compact = document.querySelector('.jiggle-bar-title-compact') as HTMLElement | null;
      const title = document.querySelector('.jiggle-bar-title') as HTMLElement;
      const bs = getComputedStyle(bar);
      return {
        barTop: bs.insetBlockStart, barBlock: bar.style.blockSize || '', barH: bar.getBoundingClientRect().height,
        rowPos: getComputedStyle(row).position,
        compactDisplay: compact ? getComputedStyle(compact).display : 'absent',
        title: title.innerText,
      };
    });
    expect(m.barTop).toBe('0px');
    expect(m.rowPos, 'chip row is not sticky above the phone tier').not.toBe('sticky');
    expect(m.compactDisplay, 'phone title hidden on wide tiers').toBe('none');
    expect(m.title).toBe(EN_FULL);
    expect(m.barH, 'bar is content-sized, not the phone fixed height').toBeLessThan(56);
    await page.screenshot({ path: `${SHOTS}/1024x600-en-edit.png` });
    await testInfo.attach('1024x600-en-edit', { path: `${SHOTS}/1024x600-en-edit.png`, contentType: 'image/png' });
  });

  test('360x740 resting sell screen and quick-button mode are unchanged', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/');
    await expect(page.locator('#buttons-grid-all .btn-tile').first()).toBeVisible();
    const top = await page.evaluate(() => ({
      top: parseFloat(getComputedStyle(document.querySelector('#browsing-category-chips')!).top),
      rem: parseFloat(getComputedStyle(document.documentElement).fontSize),
    }));
    expect(top.top, 'resting chip row keeps top: -.7rem').toBeCloseTo(-0.7 * top.rem, 1);

    await setBrowsingMode(page, 'strip_overflow');
    await page.goto('/');
    await longPress(page.locator('#buttons-grid .btn-tile').first());
    await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
    await expect(page.locator('.jiggle-bar')).not.toHaveClass(/jiggle-bar--chips/);
    await expect(page.locator('.jiggle-bar-title')).toHaveText(EN_QUICK);
  });

  test('en compact copy is what a phone shows', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/');
    await expect(page.locator('#buttons-grid-all .btn-tile').first()).toBeVisible();
    await arm(page);
    // The hidden full twin has no layout box, so innerText is just what is shown.
    await expect.poll(() => page.locator('.jiggle-bar-title').evaluate((el) => (el as HTMLElement).innerText)).toBe(EN_COMPACT);
  });
});
