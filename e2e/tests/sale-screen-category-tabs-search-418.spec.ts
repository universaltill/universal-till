import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#418: the till sale screen gets category tabs + search. Both are
// pure client-side filters over the already-rendered tile set (see
// app.css's #418 comment) — a tab switch never round-trips to the server,
// and a search query composes with the active tab rather than overriding
// it (the bug class ut-docs#419 fixes on the self-order kiosk side, where
// search used to reload the grid via a separate endpoint and silently drop
// the active category filter). Since ut-docs#2173, search and the tab
// strip occupy the same row and are mutually exclusive on screen — opening
// search hides the tab strip, and closing it (the strip's back arrow, or
// Escape) clears the query — so "composes with the active tab" now means
// within one open-search session, never across a tab switch (switching
// tabs requires closing search first). See this file's first test.
//
// Drives the real demo-seeded catalog (001_init.sql) rather than importing
// fixture data — Food (default-active tab, nests a "Dairy" subcategory
// with Butter 250g among others) and Drinks (Coca-Cola 330ml among
// others) already exist as real category-grouped shortcut tiles, same
// convention sale-screen-213.spec.ts and rtl.spec.ts already rely on for
// this shared server.
test.describe('sale screen category tabs + search (ut-docs#418)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('tabs switch which tiles show, search filters within the active tab', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    const butterTile = page.locator('.btn-tile', { hasText: 'Butter 250g' }); // Food > Dairy
    const colaTile = page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' }); // Drinks

    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });

    // Food is the default-active tab (first root category) — only its own
    // (nested) tiles show, Drinks' tiles don't.
    await expect(foodTab).toHaveClass(/active/);
    await expect(butterTile).toBeVisible();
    await expect(colaTile).toBeHidden();

    // Switching tabs is a pure client-side toggle — no request, no reload.
    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(colaTile).toBeVisible();
    await expect(butterTile).toBeHidden();

    // Search filters WITHIN the active tab: searching "Butter" while on
    // Drinks must show nothing (Butter lives in Food), not silently jump
    // tabs — the self-order compose bug (ut-docs#419) this design avoids.
    // ut-docs#2173: search is no longer always on screen, and — new since
    // that card — it now REPLACES the tab strip in place rather than
    // sitting alongside it, so a query can no longer survive a tab switch
    // (switching tabs requires closing search first, via the strip's back
    // arrow, which clears the query by design — see
    // sale-screen-search-strip-2173.spec.ts for that cycle's own
    // coverage). This still proves the ut-docs#419 property that matters:
    // search never leaks across the active tab boundary, checked fresh
    // inside each tab's own search session rather than across one that
    // spans a tab switch.
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('Butter');
    await expect(colaTile).toBeHidden();
    await expect(butterTile).toBeHidden(); // still on the Drinks tab

    await page.locator('.products-strip-back').click();
    await expect(search).toHaveValue(''); // closing search clears the query (ut-docs#2173)
    await foodTab.click();
    await expect(butterTile).toBeVisible(); // no query active, Food tab's own tiles show

    await page.locator('.products-strip-search').click();
    await search.fill('Butter');
    await expect(butterTile).toBeVisible(); // "Butter" query matches, Food tab active
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeHidden(); // same tab, doesn't match query

    await search.fill('');
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeVisible();

    assertClean();
  });

  test('a search matching nothing in the active tab shows a no-matches message, not a blank panel (ut-docs#422)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });
    const noMatches = page.locator('.products-tab-panel:visible .empty', { hasText: 'No matching products.' });

    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(noMatches).toBeHidden();

    // ut-docs#2173: open search via the strip's search icon first.
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('this matches absolutely nothing on the till');
    await expect(noMatches).toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' })).toBeHidden();

    await search.fill('');
    await expect(noMatches).toBeHidden();
    await expect(page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' })).toBeVisible();

    assertClean();
  });

  test('Farsi locale renders the tab bar + search RTL and a tile is still clickable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    // ut-docs#2173: the search box is no longer always on screen — its
    // trigger icon is, and opening it still reveals the same input, RTL
    // included. sale-screen-search-strip-2173.spec.ts covers the
    // expand/collapse cycle itself in full; this just confirms the search
    // path still works end to end under RTL.
    await expect(page.locator('.products-strip-search')).toBeVisible();
    await page.locator('.products-strip-search').click();
    await expect(page.locator('#products-search')).toBeVisible();

    // Exactly one tab is active by default, and its own tile is a real,
    // clickable hit target — logical CSS properties must not have pushed
    // it out of frame or behind another element under RTL.
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    const butterTile = page.locator('.btn-tile', { hasText: 'Butter' });
    await expect(butterTile).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      butterTile.click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Butter');

    assertClean();
  });
});
