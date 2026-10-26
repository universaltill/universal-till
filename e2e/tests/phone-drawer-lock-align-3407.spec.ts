import { test, expect } from './fixtures';
import { ensureOperator, openPhoneDrawer } from './helpers';
import type { Page } from '@playwright/test';

// ut-docs#3407: in the phone drawer the Lock row's icon and label started
// ~5px further toward the start edge than every other row, because
// `.nav-drawer .btn-lock` set a smaller inline padding than the `.nav-toggle`
// padding every other drawer row has at <=480px. The icon column must line up.

async function iconEdges(page: Page, edge: 'left' | 'right') {
  return page.locator('#nav-drawer').evaluate((drawer, e) => {
    const vis = (el: Element) => {
      const r = el.getBoundingClientRect();
      return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== 'hidden';
    };
    const edgeOf = (row: Element) => {
      const ico = row.querySelector('.nav-toggle-ico');
      return ico && vis(ico) ? ico.getBoundingClientRect()[e as 'left' | 'right'] : null;
    };
    const lockRow = drawer.querySelector('.btn-lock');
    const lock = lockRow ? edgeOf(lockRow) : null;
    const others: number[] = [];
    drawer.querySelectorAll('a.nav-toggle').forEach((a) => {
      const v = edgeOf(a);
      if (v !== null) others.push(v);
    });
    return { lock, others };
  }, edge);
}

test.describe('phone drawer: Lock row aligns with the other rows (#3407)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  for (const path of ['/', '/settings']) {
    test(`${path}: icon start edge matches (LTR)`, async ({ page }) => {
      await ensureOperator(page);
      await page.goto(path);
      await openPhoneDrawer(page);
      const { lock, others } = await iconEdges(page, 'left');
      expect(lock, 'Lock icon is visible').not.toBeNull();
      expect(others.length, 'other rows are visible').toBeGreaterThan(0);
      for (const o of others) expect(Math.abs(lock! - o), `lock ${lock} vs row ${o}`).toBeLessThanOrEqual(1);
    });
  }

  test('/: icon start edge matches (RTL)', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await openPhoneDrawer(page);
    await expect.poll(() => page.locator('#nav-drawer').evaluate((el) => getComputedStyle(el).transform)).toBe('none');
    const { lock, others } = await iconEdges(page, 'right');
    expect(lock, 'Lock icon is visible').not.toBeNull();
    expect(others.length, 'other rows are visible').toBeGreaterThan(0);
    for (const o of others) expect(Math.abs(lock! - o), `lock ${lock} vs row ${o}`).toBeLessThanOrEqual(1);
  });
});

test.describe('tablet rail: Lock padding unchanged (#3407)', () => {
  test.use({ viewport: { width: 1024, height: 768 } });

  test('rail .btn-lock keeps .3rem inline padding', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    const pad = await page.locator('.btn-lock').first().evaluate((el) => {
      const cs = getComputedStyle(el);
      return { start: parseFloat(cs.paddingInlineStart), end: parseFloat(cs.paddingInlineEnd), rem: parseFloat(getComputedStyle(document.documentElement).fontSize) };
    });
    expect(pad.start).toBeCloseTo(pad.rem * 0.3, 1);
    expect(pad.end).toBeCloseTo(pad.rem * 0.3, 1);
  });
});
