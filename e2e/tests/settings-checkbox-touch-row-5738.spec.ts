import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#5738: a label-wrapped checkbox in Settings rendered as a ~17px
// inline row with a 13px native box -- far below the 44px touch floor. One
// shared rule in app.css (`.settings-panel label:not(.staff-lang-opt):not(.ps-switch)
// :has(> input[type=checkbox])`)
// makes every such label a flex row >= 2.75rem tall with a 1.5rem box under
// body.kiosk (a body class the server adds from UT_KIOSK; the CSS keys only on
// that class, so toggling it is equivalent) or a coarse pointer.
//
// Two representative rows: the stock-tracking checkbox (its label carried an
// inline `display:block`) and the notices checkbox (a plain <label>).
// Touch itself is verified only as emulated geometry, not on real hardware.

const MIN_ROW = 43.5;
const CASES = [
  { name: 'allow negative inventory', section: 'settings-stock-tracking', input: '#allow-negative-inventory-cb' },
  { name: 'notices enabled', section: 'settings-notices', input: '[data-testid="notices-enabled"]' },
  // The row that surfaced the card (#4247's voucher-slip checkbox).
  { name: 'voucher slip', section: 'settings-printer', input: '[data-testid="printer-voucher-slip"]' },
];

async function measure(page: Page, inputSel: string) {
  return page.evaluate((sel) => {
    const input = document.querySelector<HTMLInputElement>(sel)!;
    const label = input.closest('label')!;
    const lr = label.getBoundingClientRect();
    const ir = input.getBoundingClientRect();
    // Extent of the label's own text (the node after the input).
    let textLeft = Infinity;
    let textRight = -Infinity;
    const walker = document.createTreeWalker(label, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!n.textContent || !n.textContent.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(n);
      for (const r of Array.from(range.getClientRects())) {
        textLeft = Math.min(textLeft, r.left);
        textRight = Math.max(textRight, r.right);
      }
    }
    const cx = lr.left + lr.width / 2;
    const cy = lr.top + lr.height / 2;
    const hit = document.elementFromPoint(cx, cy);
    return {
      rtl: document.documentElement.dir === 'rtl',
      label: { x: lr.left, y: lr.top, w: lr.width, h: lr.height },
      box: { left: ir.left, right: ir.right, w: ir.width, h: ir.height },
      textLeft,
      textRight,
      hitOk: !!hit && (hit === label || hit === input || label.contains(hit)),
    };
  }, inputSel);
}

for (const lang of ['en', 'fa']) {
  for (const c of CASES) {
    test(`${c.name} is a >=44px touch row, box at inline-start (${lang}, kiosk 1024x600)`, async ({ page }) => {
      await page.setViewportSize({ width: 1024, height: 600 });
      await page.goto(`/settings?lang=${lang}#${c.section}`);
      if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      await page.evaluate(() => document.body.classList.add('kiosk'));
      const input = page.locator(c.input);
      await input.scrollIntoViewIfNeeded();
      await expect(input).toBeVisible();

      const m = await measure(page, c.input);
      expect(m.label.h, 'label border-box height').toBeGreaterThanOrEqual(MIN_ROW);
      expect(m.box.w, 'checkbox box width').toBeGreaterThan(20);
      expect(m.box.h, 'checkbox box height').toBeGreaterThan(20);
      if (m.rtl) {
        expect(m.box.right, 'RTL: box right edge is on the inline-start side of the text').toBeGreaterThan(m.textRight - 0.5);
      } else {
        expect(m.box.left, 'LTR: box left edge is before the text').toBeLessThan(m.textLeft + 0.5);
        expect(m.box.right, 'LTR: box ends before the text starts').toBeLessThanOrEqual(m.textLeft + 0.5);
      }
      expect(m.hitOk, 'elementFromPoint at the label centre hits the label or its input').toBe(true);
    });
  }
}

// The same rule's `@media (pointer: coarse)` copy: a touch context (no
// body.kiosk) must get the same row; a desktop mouse context keeps its density.
test.describe('touch pointer (no kiosk class)', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 1024, height: 600 } });
  test('notices row is a >=44px touch row under pointer: coarse', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-notices');
    expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
    expect(await page.evaluate(() => document.body.classList.contains('kiosk'))).toBe(false);
    const input = page.locator('[data-testid="notices-enabled"]');
    await input.scrollIntoViewIfNeeded();
    await expect(input).toBeVisible();
    const m = await measure(page, '[data-testid="notices-enabled"]');
    expect(m.label.h, 'label border-box height').toBeGreaterThanOrEqual(MIN_ROW);
    expect(m.box.w, 'checkbox box width').toBeGreaterThan(20);
  });
});

test('desktop mouse keeps the compact row (rule is gated)', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/settings?lang=en#settings-notices');
  expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(false);
  const input = page.locator('[data-testid="notices-enabled"]');
  await input.scrollIntoViewIfNeeded();
  await expect(input).toBeVisible();
  const m = await measure(page, '[data-testid="notices-enabled"]');
  expect(m.label.h, 'desktop label stays compact').toBeLessThan(MIN_ROW);
});
