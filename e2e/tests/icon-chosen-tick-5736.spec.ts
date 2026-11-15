import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5736: a chosen built-in icon tile (aria-selected="true") carries a
// shape cue (a tick badge in the top inline-end corner), not only the accent
// border/fill that is hue-only next to the same-hue focus outline (WCAG
// 1.4.1). The badge is two pseudo-elements: ::after = filled circle,
// ::before = the check (clip-path). Chromium computed-style assertions.

const DIALOG = '#category-dialog';
const GRID = '#category-icon-grid';

async function openCategory(page: Page) {
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
}

type Badge = { circle: string; check: string; pos: string; w: number; bg: string; checkBg: string; clip: string };
const badge = (el: Element): Badge => {
  const a = getComputedStyle(el, '::after');
  const b = getComputedStyle(el, '::before');
  return {
    circle: a.content, check: b.content, pos: a.position, w: parseFloat(a.width) || 0,
    bg: a.backgroundColor, checkBg: b.backgroundColor, clip: b.clipPath,
  };
};
const hasBadge = (b: Badge) => b.circle !== 'none' && b.check !== 'none' && b.pos === 'absolute' && b.w > 8
  && b.bg !== 'rgba(0, 0, 0, 0)' && b.clip !== 'none';

async function assertTick(page: Page) {
  const grid = page.locator(GRID);
  const on = grid.locator('[data-icon="coffee"]');
  const other = grid.locator('[data-icon="tea"]');
  await on.click();
  await expect(on).toHaveAttribute('aria-selected', 'true');
  expect(hasBadge(await on.evaluate(badge)), 'chosen tile has the tick').toBe(true);
  expect(hasBadge(await other.evaluate(badge)), 'unchosen tile has none').toBe(false);
  // Focus another tile without choosing it: focus ring there, tick stays on the chosen one only.
  await on.focus();
  await page.keyboard.press('ArrowRight');
  const focused = page.locator(`${GRID} :focus`);
  await expect(focused).not.toHaveAttribute('data-icon', 'coffee');
  await expect(focused).toHaveAttribute('aria-selected', 'false');
  expect(hasBadge(await focused.evaluate(badge)), 'focused-but-unchosen: no tick').toBe(false);
  expect(await focused.evaluate((e) => getComputedStyle(e).outlineStyle)).not.toBe('none');
  expect(hasBadge(await on.evaluate(badge)), 'chosen keeps tick while focus is elsewhere').toBe(true);
  // Chosen AND focused: both.
  await on.focus();
  await expect(on).toBeFocused();
  expect(hasBadge(await on.evaluate(badge))).toBe(true);
  expect(await on.evaluate((e) => getComputedStyle(e).outlineStyle)).not.toBe('none');
}

test('normal: chosen tile shows a tick; unchosen and focused-only do not', async ({ page }) => {
  const assertClean = watchConsole(page);
  await openCategory(page);
  await assertTick(page);
  assertClean();
});

test('"No image" tile gets the tick when chosen', async ({ page }) => {
  await openCategory(page);
  const none = page.locator(`${GRID} [data-icon="none"]`);
  await none.click();
  await expect(none).toHaveAttribute('aria-selected', 'true');
  expect(hasBadge(await none.evaluate(badge))).toBe(true);
});

test('Light effects level: the tick survives the box-shadow/filter wipe', async ({ page }) => {
  const res = await page.request.post('/api/settings/effects-level', { form: { level: 'light' } });
  expect(res.status()).toBe(204);
  await openCategory(page);
  await expect(page.locator('html')).toHaveClass(/(^|\s)fx-light(\s|$)/);
  await assertTick(page);
  await page.request.post('/api/settings/effects-level', { form: { level: 'full' } });
});

test('forced colors: tick is drawn in system colours and differs from the tile', async ({ page }, testInfo) => {
  await page.emulateMedia({ forcedColors: 'active' });
  await openCategory(page);
  expect(await page.evaluate(() => matchMedia('(forced-colors: active)').matches)).toBe(true);
  const on = page.locator(`${GRID} [data-icon="coffee"]`);
  await on.click();
  const r = { b: await on.evaluate(badge), tile: await on.evaluate((el) => getComputedStyle(el).backgroundColor) };
  expect(hasBadge(r.b)).toBe(true);
  expect(r.b.bg).not.toBe(r.tile);
  expect(r.b.checkBg).not.toBe(r.b.bg);
  await testInfo.attach('icon-tick-forced-colors.png', { body: await on.screenshot(), contentType: 'image/png' });

});

test('RTL: the tick sits in the top inline-end (left) corner, clear of the label', async ({ page }) => {
  await openCategory(page);
  await page.evaluate(() => document.documentElement.setAttribute('dir', 'rtl'));
  const on = page.locator(`${GRID} [data-icon="coffee"]`);
  await on.click();
  const g = await on.evaluate((el) => {
    const t = el.getBoundingClientRect();
    const cs = getComputedStyle(el, '::after');
    const lbl = el.querySelector('span')!.getBoundingClientRect();
    const w = parseFloat(cs.width);
    // Resolved insets relative to the tile box.
    const left = t.left + parseFloat(cs.left);
    const top = t.top + parseFloat(cs.top);
    return { tl: t.left, tt: t.top, tw: t.width, left, top, w, lblTop: lbl.top };
  });
  expect(g.left - g.tl).toBeLessThan(g.tw / 2);
  expect(g.top - g.tt).toBeLessThan(10);
  expect(g.top + g.w, 'badge ends above the label').toBeLessThanOrEqual(g.lblTop);
});

test('negative: colour tiles and non-icon picker tiles get no ::after tick', async ({ page }) => {
  await page.goto('/catalog');
  await page.locator('#item-form-add-btn').click();
  const tile = page.locator('#item-color-grid .item-color-tile').nth(2);
  await tile.click();
  await expect(tile).toHaveAttribute('aria-checked', 'true');
  expect(hasBadge(await tile.evaluate(badge))).toBe(false);
  // A plain .picker-tile (setup/order-type style) that is aria-selected or
  // aria-pressed must stay badge-free: fails if the rule is ever widened
  // from .builtin-icon-tile to .picker-tile.
  const plain = await page.evaluate(() => {
    const out: string[] = [];
    for (const attr of ['aria-selected', 'aria-pressed']) {
      const b = document.createElement('button');
      b.className = 'picker-tile';
      b.setAttribute(attr, 'true');
      document.body.appendChild(b);
      out.push(getComputedStyle(b, '::after').content, getComputedStyle(b, '::before').content);
      b.remove();
    }
    return out;
  });
  expect(plain).toEqual(['none', 'none', 'none', 'none']);
});
