import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#2876: a buttons-changed / modifiers-changed refresh makes the
// sale-screen grid root (`.pos-container > .products`, hx-swap=outerHTML)
// re-fetch itself. restoreGridState() put the selected tab and search back
// but not the scroll offset, so since ut-docs#2765 (the open sale screen
// polls and fires buttons-changed when the catalog changes on another
// device) an operator scrolled down a long tab was yanked to the top.
// 1024x480 is chosen, as in products-scroll-affordance-1313, because the
// default tab then reliably overflows `.products`.
test.use({ viewport: { width: 1024, height: 480 } });

const PRODUCTS = '.pos-container > .products';

// Fire buttons-changed on body and wait for the grid's own GET + the swap
// (the old root is detached and a new one is attached).
async function refreshGrid(page: Page) {
  await page.evaluate(() => {
    const el = document.querySelector('.pos-container > .products') as HTMLElement & { __ut2876?: boolean };
    el.__ut2876 = true;
  });
  await Promise.all([
    page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname === '/ui/buttons'),
    page.evaluate(() => (window as unknown as { htmx: { trigger(e: Element, n: string): void } }).htmx.trigger(document.body, 'buttons-changed')),
  ]);
  await page.waitForFunction(() => {
    const el = document.querySelector('.pos-container > .products') as (HTMLElement & { __ut2876?: boolean }) | null;
    return !!el && !el.__ut2876;
  });
  await waitForStableLayout(page, PRODUCTS);
}

const top = (page: Page) => page.locator(PRODUCTS).evaluate((el) => el.scrollTop);

test.describe('sale grid keeps its scroll across a buttons-changed refresh (ut-docs#2876)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  async function open(page: Page) {
    await page.goto('/');
    await page.waitForSelector(PRODUCTS);
    await waitForStableLayout(page, PRODUCTS);
    const overflow = await page.locator(PRODUCTS).evaluate((el) => el.scrollHeight - el.clientHeight);
    expect(overflow, 'default tab must overflow for this spec to mean anything').toBeGreaterThan(60);
    return overflow;
  }

  test('scrollTop survives the refresh; scrolling back to top is respected on the next one', async ({ page }) => {
    const assertClean = watchConsole(page);
    const overflow = await open(page);
    const target = Math.min(Math.floor(overflow / 2), 150);
    await page.locator(PRODUCTS).evaluate((el, t) => { el.scrollTop = t; }, target);
    const before = await top(page);
    expect(before).toBeGreaterThan(40);

    await refreshGrid(page);
    expect(Math.abs((await top(page)) - before), 'scrollTop kept after refresh').toBeLessThanOrEqual(2);

    // Not resurrected: the user scrolls back to the top, refreshes again.
    await page.locator(PRODUCTS).evaluate((el) => { el.scrollTop = 0; });
    await refreshGrid(page);
    expect(await top(page), 'old offset must not come back').toBeLessThanOrEqual(2);

    // And a refresh with nothing scrolled stays at the top, a third time.
    await refreshGrid(page);
    expect(await top(page)).toBeLessThanOrEqual(2);
    assertClean();
  });

  test('a captured offset is consumed once and never applied to another tab or a stale capture', async ({ page }) => {
    await open(page);
    await page.locator(PRODUCTS).evaluate((el) => { el.scrollTop = 100; });
    await refreshGrid(page);
    expect(await top(page)).toBeGreaterThan(40);

    // After the restore the stashed offset is gone (consumed).
    const left = await page.evaluate(() => (window as unknown as { utSaleGridState: { scroll?: unknown } }).utSaleGridState.scroll);
    expect(left ?? null, 'scroll capture consumed after restore').toBeNull();

    // A leftover capture for a DIFFERENT tab, or an old one, must not move a
    // fresh grid (boosted navigation keeps window.utSaleGridState alive).
    for (const variant of ['other-tab', 'stale']) {
      await page.locator(PRODUCTS).evaluate((el) => { el.scrollTop = 0; });
      const moved = await page.evaluate((v) => {
        const w = window as unknown as { utSaleGridState: Record<string, unknown>; Alpine: { $data(e: Element): { tab: string; restoreGridState(): void } } };
        const data = w.Alpine.$data(document.querySelector('.products-finder')!);
        w.utSaleGridState = {
          ...w.utSaleGridState,
          tab: data.tab,
          scroll: { tab: v === 'other-tab' ? '__not-this-tab__' : data.tab, top: 120, at: v === 'stale' ? Date.now() - 60_000 : Date.now() },
        };
        data.restoreGridState();
        return new Promise<number>((res) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(() => res((document.querySelector('.pos-container > .products') as HTMLElement).scrollTop), 50))));
      }, variant);
      expect(moved, `${variant} capture must not scroll the grid`).toBeLessThanOrEqual(2);
    }
  });
});
