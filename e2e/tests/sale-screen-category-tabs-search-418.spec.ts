import { test, expect } from './fixtures';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#418: the till sale screen gets category tabs + search.
//
// ut-docs#2181 SUPERSEDED this file's original #419 invariant. #418/#422
// originally scoped a search query to the active tab only ("search never
// leaks across the active tab boundary") — deliberately, to avoid the
// self-order kiosk's own #419 bug class, where search reloaded the grid via
// a separate endpoint and silently dropped the active category filter.
// Once ut-docs#2173 made search REPLACE the tab strip in place (rather than
// sitting alongside it), that scoping stopped being a safe default and
// became a dead end instead: with the strip off-screen and the query
// cleared on every tab switch, an item living in a category other than
// whichever tab happened to be active was simply unfindable — "No matching
// products." with no way to widen the search from where the operator was
// standing. ut-docs#2181's own product-owner reference (SumUp) searches the
// whole catalogue, not the active category, so that is now this pipeline's
// behaviour too.
//
// ut-docs#2294 SUPERSEDED the "search is a client-side filter over
// already-rendered tiles" mechanic ut-docs#2181 gave search, and
// ut-docs#2613 retired the strip's All tab (ut-docs#2212/#2294) outright —
// the strip is category tabs plus the "…" button only, the first category
// tab selected by default:
//   - Search is a real, debounced server round trip (GET /ui/buttons/search)
//     into its OWN #search-results grid — not a filter toggled over the
//     tab tiles already in the DOM. While a query is active, #buttons-grid
//     (every tab panel) hides in its entirety; Alpine never removes it from the DOM (x-show only), so a
//     plain by-name tile locator now matches more than one node at once —
//     tests below scope to the one container that's actually meant to be
//     showing, rather than disambiguating with ":visible" everywhere.
//
// Drives the real demo-seeded catalog (001_init.sql) rather than importing
// fixture data — Food (nests a "Dairy"
// subcategory with Butter 250g among others) and Drinks (Coca-Cola 330ml
// among others) already exist as real category-grouped shortcut tiles, same
// convention sale-screen-213.spec.ts and rtl.spec.ts already rely on for
// this shared server.
test.describe('sale screen category tabs + search (ut-docs#418)', () => {
  // ut-docs#2499: the category strip (and its "..." overflow) is now ONE of
  // three selectable sell-screen browsing modes (Settings -> Sell screen,
  // sale.browsing_mode) and no longer the unconditional default (that is
  // category tiles) -- this file is about the strip, so it picks
  // strip_overflow explicitly rather than assuming it. worker-till.ts
  // already boots every worker till in this mode; this is what makes the
  // dependency visible in the spec itself.
  test.beforeEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('tabs switch which tiles show with no query active; a query returns results across every category (ut-docs#2181)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();

    const butterInFoodPanel = page.locator('#cat-panel-cat_food .btn-tile', { hasText: 'Butter 250g' });
    const colaInDrinksPanel = page.locator('#cat-panel-cat_drink .btn-tile', { hasText: 'Coca-Cola 330ml' });

    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });

    // ut-docs#2613: no All tab and no All grid — the first tab is a real
    // category tab and it is the one selected by default, with no query
    // and no prior tap.
    await expect(page.locator('#cat-tab-all')).toHaveCount(0);
    await expect(page.locator('#buttons-grid-all')).toHaveCount(0);
    await expect(tabBar.locator('.tab').first()).toHaveAttribute('data-cat-tab', '');
    await expect(tabBar.locator('.tab').first()).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);

    // Selecting a category tab narrows to just that category's own panel.
    await foodTab.click();
    await expect(foodTab).toHaveClass(/active/);
    await expect(butterInFoodPanel).toBeVisible();
    await expect(colaInDrinksPanel).toBeHidden();

    // Switching tabs is a pure client-side toggle — no request, no reload.
    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(colaInDrinksPanel).toBeVisible();
    await expect(butterInFoodPanel).toBeHidden();

    // Open search while on Drinks, then search for something that only
    // lives under Food > Dairy. ut-docs#2181's promise this preserves: the
    // query spans every category, so Butter is findable without leaving
    // the Drinks tab — ut-docs#2294 moved the mechanism to a real server
    // round trip into #search-results (not a client-side filter over the
    // tab tiles, which stay hidden — and in the DOM, unmatched —
    // the whole time a query is active).
    const searchResults = page.locator('#search-results');
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('Butter');
    await expect(page.locator('#buttons-grid')).toBeHidden();
    await expect(searchResults.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();
    await expect(searchResults.locator('.btn-tile', { hasText: 'Coca-Cola Can 330ml' })).toHaveCount(0);

    // The reverse direction: search for a Drinks item while its own tab
    // isn't active.
    await search.fill('Cola');
    await expect(searchResults.locator('.btn-tile', { hasText: 'Coca-Cola Can 330ml' })).toBeVisible();
    await expect(searchResults.locator('.btn-tile', { hasText: 'Butter 250g' })).toHaveCount(0);

    // Closing search clears the query (ut-docs#2173) and restores the
    // active-tab-only view — Drinks stays selected, exactly as it was
    // before search opened; closeSearch() only ever resets search state.
    await page.locator('.products-strip-back').click();
    await expect(search).toHaveValue('');
    await expect(page.locator('#buttons-grid')).toBeVisible();
    await expect(colaInDrinksPanel).toBeVisible(); // back to plain Drinks-tab view

    // A query still narrows within a single category too, same as before.
    await foodTab.click();
    await page.locator('.products-strip-search').click();
    await search.fill('Butter');
    await expect(searchResults.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();
    await expect(searchResults.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toHaveCount(0);

    await search.fill('');
    await expect(page.locator('#cat-panel-cat_food .btn-tile', { hasText: 'Cheddar Cheese' })).toBeVisible();

    assertClean();
  });

  test('the strip has no All tab: switching between category tabs never grows the strip row (ut-docs#2613)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    const strip = page.locator('.products-strip');
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });
    await expect(tabBar).toBeVisible();
    await expect(tabBar.getByRole('tab', { name: 'All', exact: true })).toHaveCount(0);

    // ut-docs#2173's own invariant (same row, same height, never a second
    // row) must hold across tab switches.
    const beforeBox = await strip.boundingBox();
    expect(beforeBox, 'strip must have a measurable box on first paint').toBeTruthy();

    await foodTab.click();
    await expect(foodTab).toHaveClass(/active/);
    // Every active Food item is still on the strip (ut-docs#2541 implicit
    // tiles), nested Dairy included, with no All grid to fall back on.
    await expect(page.locator('#cat-panel-cat_food .btn-tile', { hasText: 'Butter 250g' })).toBeVisible();
    await expect(page.locator('#cat-panel-cat_drink')).toBeHidden();
    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    await expect(page.locator('#cat-panel-cat_food')).toBeHidden();

    const afterBox = await strip.boundingBox();
    expect(afterBox, 'strip must have a measurable box after switching tabs').toBeTruthy();
    expect(
      Math.abs(afterBox!.height - beforeBox!.height),
      `strip height must not change across tab switches (before ${beforeBox!.height}px, after ${afterBox!.height}px)`,
    ).toBeLessThan(1);

    assertClean();
  });

  test('a search matching nothing in the whole catalogue shows exactly one no-matches message (ut-docs#422, scope widened by ut-docs#2181, moved server-side by ut-docs#2294)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });
    // ut-docs#2294: the "no matches" message moved into #search-results
    // (ButtonsHTTP.Search's own "products-search-results" template) — the
    // OLD #buttons-grid > .empty message this test used to pin is now
    // unreachable while a query is active (#buttons-grid, and everything
    // under it, hides in its entirety — see this file's first test).
    const noMatches = page.locator('#search-results .empty', { hasText: 'No matching products.' });

    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(noMatches).toHaveCount(0);

    // ut-docs#2173: open search via the strip's search icon first.
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('this matches absolutely nothing on the till');
    // Exactly one message for the whole catalogue (ut-docs#2181) — the
    // server searches every active item regardless of category now, so
    // there's no longer a per-tab-panel message to duplicate it across.
    await expect(noMatches).toHaveCount(1);
    await expect(noMatches).toBeVisible();
    await expect(page.locator('#search-results .btn-tile', { hasText: 'Coca-Cola Can 330ml' })).toHaveCount(0);

    // A query matching something in a DIFFERENT category than the one
    // active when search opened must still clear the message — proving the
    // check spans every category, not just Drinks.
    await search.fill('Butter'); // Food > Dairy, not Drinks
    await expect(noMatches).toHaveCount(0);
    await expect(page.locator('#search-results .btn-tile', { hasText: 'Butter 250g' })).toBeVisible();

    await search.fill('');
    await expect(page.locator('#search-results')).toBeHidden();

    assertClean();
  });

  test('Farsi locale renders the tab bar + search RTL and a tile is still clickable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    // ut-docs#2613: no All tab — pick Food (before opening search, which
    // hides the tab bar) so Butter's tile is on screen.
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    await foodTab.click();
    await expect(foodTab).toHaveClass(/active/);
    // ut-docs#2173: the search box is no longer always on screen — its
    // trigger icon is, and opening it still reveals the same input, RTL
    // included. sale-screen-search-strip-2173.spec.ts covers the
    // expand/collapse cycle itself in full; this just confirms the search
    // path still works end to end under RTL.
    await expect(page.locator('.products-strip-search')).toBeVisible();
    await page.locator('.products-strip-search').click();
    await expect(page.locator('#products-search')).toBeVisible();

    // Exactly one tab is active, and its panel's tile is a real, clickable
    // hit target — logical CSS properties must not have pushed it out of
    // frame or behind another element under RTL.
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    const butterTile = page.locator('#cat-panel-cat_food .btn-tile', { hasText: 'Butter 250g' });
    await expect(butterTile).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      butterTile.click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Butter');

    assertClean();
  });
});
