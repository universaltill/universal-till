import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5852: till-side stock-take of one location on /inventory.
// Count mode swaps #stock-levels-card in place (no dialog), keeps typed
// counts across the search filter, reviews only counted lines and applies
// them all-or-nothing through POST /api/inventory/stock-take.

const row = (page: Page, name: string) => page.locator(`#stocktake-table tr.stocktake-row[data-name="${name}"]`);
const input = (page: Page, name: string) => row(page, name).locator('.stocktake-input');

async function startCounting(page: Page) {
  await page.goto('/inventory');
  // The location picker only appears after Stock-take is pressed.
  await expect(page.locator('#stocktake-picker')).toBeHidden();
  await page.locator('#stocktake-start').click();
  // With two or more locations a picker is offered first (inline row).
  const begin = page.locator('#stocktake-begin');
  if (await begin.isVisible()) await begin.click();
  await expect(page.locator('#stocktake-table')).toBeVisible();
}

test.describe('till stock-take (ut-docs#5852)', () => {
  test('count, filter, review and apply', async ({ page }) => {
    const assertClean = watchConsole(page);
    await startCounting(page);

    // Mode is visible and nothing is modal.
    await expect(page.locator('#stocktake-title .chip')).toBeVisible();
    expect(await page.evaluate(() => document.querySelectorAll(':modal').length)).toBe(0);
    await expect(page.locator('#stocktake-review-btn')).toBeDisabled();

    await input(page, 'Pepsi Can 330ml').fill('7');
    await input(page, 'Bananas').fill('1,5'); // weighed item, decimal comma
    await expect(page.locator('#stocktake-badge')).toContainText('2');
    await expect(page.locator('#stocktake-review-btn')).toBeEnabled();

    // Search hides rows but never loses a typed count.
    await page.locator('#stock-search').fill('Bananas');
    await expect(row(page, 'Pepsi Can 330ml')).toBeHidden();
    await page.locator('#stock-search').fill('');
    await expect(input(page, 'Pepsi Can 330ml')).toHaveValue('7');
    await expect(page.locator('#stocktake-badge')).toContainText('2');

    // A bad entry blocks Review and is flagged in text, not colour alone.
    await input(page, 'Pepsi Can 330ml').fill('abc');
    await expect(input(page, 'Pepsi Can 330ml')).toHaveAttribute('aria-invalid', 'true');
    await expect(page.locator('#stocktake-review-btn')).toBeDisabled();
    await input(page, 'Pepsi Can 330ml').fill('7');
    await expect(page.locator('#stocktake-review-btn')).toBeEnabled();

    await page.locator('#stocktake-review-btn').click();
    const rows = page.locator('#stocktake-review tbody tr');
    await expect(rows).toHaveCount(2); // only counted lines
    await expect(page.locator('#stocktake-review')).toContainText('1.5');

    // Back keeps everything; then Apply.
    await page.locator('#stocktake-back').click();
    await expect(input(page, 'Bananas')).toHaveValue('1,5');
    await page.locator('#stocktake-review-btn').click();
    await page.locator('#stocktake-apply').click();

    await expect(page.locator('#stocktake-result')).toBeVisible();
    await expect(page.locator('#stocktake-result')).toContainText('Counted: 2');
    // Back to the normal table, refreshed in place.
    await expect(page.locator('#stock-table')).toBeVisible();
    await expect(page.locator('#stocktake-table')).toHaveCount(0);
    await expect(page.locator('#stock-table tr.stock-row[data-name="Pepsi Can 330ml"] td').nth(3)).toHaveText('7');
    await expect(page.locator('#stock-table tr.stock-row[data-name="Bananas"] td').nth(3)).toHaveText('1.5');
    await expect(page.locator('#low-stock-card')).toBeVisible();
    assertClean();
  });

  test('cancel with counts asks first; with none leaves at once', async ({ page }) => {
    await startCounting(page);
    await page.locator('#stocktake-cancel').click();
    await expect(page.locator('#stocktake-table')).toHaveCount(0); // nothing typed: no question

    await startCounting(page);
    await input(page, 'Pepsi Can 330ml').fill('3');
    await page.locator('#stocktake-cancel').click();
    await expect(page.locator('#stocktake-confirm')).toBeVisible();
    expect(await page.evaluate(() => document.querySelectorAll(':modal').length)).toBe(0);
    await page.locator('#stocktake-keep').click();
    await expect(input(page, 'Pepsi Can 330ml')).toHaveValue('3');
    await page.locator('#stocktake-cancel').click();
    await page.locator('#stocktake-confirm-go').click();
    await expect(page.locator('#stocktake-table')).toHaveCount(0);
    await expect(page.locator('#stock-table')).toBeVisible();
  });

  test('leaving through a link with counts asks inline', async ({ page }) => {
    await startCounting(page);
    await input(page, 'Pepsi Can 330ml').fill('3');
    await page.locator('a[href="/reports"], a[href="/settings"], a[href="/"]').first().click();
    await expect(page.locator('#stocktake-confirm')).toBeVisible();
    await expect(page).toHaveURL(/\/inventory/);
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
    test(`no horizontal scroll and 44px targets at ${vp.width}x${vp.height}`, async ({ page }) => {
      await page.setViewportSize(vp);
      await startCounting(page);
      await input(page, 'Pepsi Can 330ml').fill('3');
      const noScroll = async () =>
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
      await noScroll();
      const h = await input(page, 'Pepsi Can 330ml').evaluate((e) => e.getBoundingClientRect().height);
      expect(h).toBeGreaterThanOrEqual(44);
      // The count list is long, so the action bar is stuck at the bottom:
      // it must sit above the status bar, not under it.
      const rb = await page.evaluate(() => ({
        btn: document.getElementById('stocktake-review-btn')!.getBoundingClientRect().bottom,
        status: document.querySelector('footer.statusbar')!.getBoundingClientRect().top }));
      expect(rb.btn, `Review bottom ${rb.btn} vs status bar top ${rb.status}`).toBeLessThanOrEqual(rb.status);
      await page.locator('#stocktake-review-btn').click();
      await noScroll();
      const bh = await page.locator('#stocktake-apply').evaluate((e) => e.getBoundingClientRect().height);
      expect(bh).toBeGreaterThanOrEqual(44);
      // Tester (ut-docs#5852): the action bar is fully above the status bar,
      // and the review table stays inside its card.
      const g = await page.evaluate(() => {
        const r = (sel: string) => document.querySelector(sel)!.getBoundingClientRect();
        const card = r('#stock-levels-card'), table = r('#stocktake-review table');
        return { apply: r('#stocktake-apply').bottom, status: r('footer.statusbar').top,
          tL: table.left, tR: table.right, cL: card.left, cR: card.right };
      });
      expect(g.apply, `Apply bottom ${g.apply} vs status bar top ${g.status}`).toBeLessThanOrEqual(g.status);
      expect(g.tR, `review table right ${g.tR} vs card right ${g.cR}`).toBeLessThanOrEqual(g.cR + 0.5);
      expect(g.tL, `review table left ${g.tL} vs card left ${g.cL}`).toBeGreaterThanOrEqual(g.cL - 0.5);
    });
  }
});
