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
// ut-docs#2294 SUPERSEDED both the "All reuses every category panel"
// mechanic ut-docs#2212 gave the All tab AND the "search is a client-side
// filter over already-rendered tiles" mechanic ut-docs#2181 gave search —
// see web/ui/partials/buttons.html's own panelVisible()/showAllGrid()
// comments (around its "products-finder" x-data block) for the exact
// reasoning this file's tests below are now written against:
//   - All has its OWN dedicated, flat grid (#buttons-grid-all — every
//     ACTIVE catalog item, not just quick-button ones, A-Z, no per-category
//     grouping/headers), and a category's own panel is hidden the whole
//     time All is selected — never both an All-grid copy AND a
//     category-panel copy of the same item visible at once.
//   - Search is a real, debounced server round trip (GET /ui/buttons/search)
//     into its OWN #search-results grid — not a filter toggled over the
//     tab/All-grid tiles already in the DOM. While a query is active,
//     #buttons-grid (every tab AND the All grid alike) hides in its
//     entirety; Alpine never removes it from the DOM (x-show only), so a
//     plain by-name tile locator now matches more than one node at once —
//     tests below scope to the one container that's actually meant to be
//     showing, rather than disambiguating with ":visible" everywhere.
//
// Drives the real demo-seeded catalog (001_init.sql) rather than importing
// fixture data — Food (default-active tab pre-#2294, nests a "Dairy"
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

    const allGrid = page.locator('#buttons-grid-all');
    const butterInAll = allGrid.locator('.btn-tile', { hasText: 'Butter 250g' }); // Food > Dairy
    const colaInAll = allGrid.locator('.btn-tile', { hasText: 'Coca-Cola Can 330ml' }); // Drinks, direct
    const butterInFoodPanel = page.locator('#cat-panel-cat_food .btn-tile', { hasText: 'Butter 250g' });
    const colaInDrinksPanel = page.locator('#cat-panel-cat_drink .btn-tile', { hasText: 'Coca-Cola 330ml' });

    const allTab = tabBar.getByRole('tab', { name: 'All' });
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });

    // ut-docs#2212: "All" is the first tab and is selected by default, with
    // no query and no prior tap. ut-docs#2294: unlike the original #2212
    // mechanic (every category panel visible at once), All now shows its
    // own dedicated grid — every category's own panel stays hidden the
    // whole time.
    await expect(tabBar.locator('.tab').first()).toHaveId('cat-tab-all');
    await expect(allTab).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    await expect(allGrid).toBeVisible();
    await expect(butterInAll).toBeVisible();
    await expect(colaInAll).toBeVisible();
    await expect(page.locator('#cat-panel-cat_food')).toBeHidden();
    await expect(page.locator('#cat-panel-cat_drink')).toBeHidden();

    // Selecting a real category tab still narrows to just that category —
    // the pre-#2212 behavior, now reached by an explicit tap on the tab
    // rather than being the default. The All grid hides entirely; the
    // selected category's own panel copy takes over.
    await foodTab.click();
    await expect(foodTab).toHaveClass(/active/);
    await expect(allGrid).toBeHidden();
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
    // tab/All-grid tiles, which stay hidden — and in the DOM, unmatched —
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

  test('the All tab shows a flat grid of every active item in its own dedicated grid, without growing the strip row (ut-docs#2294)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    const strip = page.locator('.products-strip');
    const allTab = tabBar.getByRole('tab', { name: 'All' });
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const allGrid = page.locator('#buttons-grid-all');
    const butterInAll = allGrid.locator('.btn-tile', { hasText: 'Butter 250g' }); // Food > Dairy
    const colaInAll = allGrid.locator('.btn-tile', { hasText: 'Coca-Cola Can 330ml' }); // Drinks, direct

    // ut-docs#2173's own invariant (same row, same height, never a second
    // row) must still hold once a tab is ADDED to the strip, not just when
    // search toggles within it.
    const beforeBox = await strip.boundingBox();
    expect(beforeBox, 'strip must have a measurable box on first paint').toBeTruthy();

    // Navigate away from the default (Food), then back to All — proving
    // selection, not just the initial default, restores the whole-catalogue
    // view.
    await foodTab.click();
    await expect(allGrid).toBeHidden();
    await allTab.click();
    await expect(allTab).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);

    // ut-docs#2294 SUPERSEDES the original ut-docs#2212 mechanic this test
    // used to pin ("every category panel visible at once, each labelled"):
    // All now has its own dedicated, flat grid — every ACTIVE catalog item
    // (not just quick-button ones), A-Z, with NO per-category
    // grouping/headers at all (unlike the old reused-panels view, or the
    // cross-category search view, both of which showed a category header
    // per section) — and every category's own panel stays hidden the whole
    // time, never showing the same item twice at once.
    await expect(butterInAll).toBeVisible();
    await expect(colaInAll).toBeVisible();
    await expect(page.locator('#cat-panel-cat_food')).toBeHidden();
    await expect(page.locator('#cat-panel-cat_drink')).toBeHidden();
    await expect(allGrid.locator('.category-header')).toHaveCount(0);

    const afterBox = await strip.boundingBox();
    expect(afterBox, 'strip must have a measurable box with All selected').toBeTruthy();
    expect(
      Math.abs(afterBox!.height - beforeBox!.height),
      `strip height must not change when All is selected (before ${beforeBox!.height}px, after ${afterBox!.height}px)`,
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
    // ut-docs#2173: the search box is no longer always on screen — its
    // trigger icon is, and opening it still reveals the same input, RTL
    // included. sale-screen-search-strip-2173.spec.ts covers the
    // expand/collapse cycle itself in full; this just confirms the search
    // path still works end to end under RTL.
    await expect(page.locator('.products-strip-search')).toBeVisible();
    await page.locator('.products-strip-search').click();
    await expect(page.locator('#products-search')).toBeVisible();

    // Exactly one tab is active by default (All, ut-docs#2294), and its
    // own dedicated grid's tile is a real, clickable hit target — logical
    // CSS properties must not have pushed it out of frame or behind
    // another element under RTL.
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    const butterTile = page.locator('#buttons-grid-all .btn-tile', { hasText: 'Butter' });
    await expect(butterTile).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      butterTile.click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Butter');

    assertClean();
  });
});
