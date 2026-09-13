import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#418: the till sale screen gets category tabs + search. Both are
// pure client-side filters over the already-rendered tile set (see
// app.css's #418 comment) — a tab switch never round-trips to the server.
//
// ut-docs#2181 SUPERSEDES this file's original #419 invariant. #418/#422
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
// behaviour too: while a query is active, EVERY category's matches show at
// once, each still clearly labelled by category (a top-level category's own
// items now carry a category header exactly while a query is active; a
// nested subcategory like Dairy already always had one). This is a
// deliberate reversal of the old invariant, not a regression — see the
// first test below for the new contract.
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

  test('tabs switch which tiles show with no query active; a query spans every category at once (ut-docs#2181)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    const butterTile = page.locator('.btn-tile', { hasText: 'Butter 250g' }); // Food > Dairy
    const colaTile = page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' }); // Drinks, direct (no subcategory)

    const allTab = tabBar.getByRole('tab', { name: 'All' });
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });

    // ut-docs#2212: "All" is the first tab and is selected by default, with
    // no query and no prior tap — every category's tiles show at once, and
    // no other single tab claims to be active.
    await expect(tabBar.locator('.tab').first()).toHaveId('cat-tab-all');
    await expect(allTab).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);
    await expect(butterTile).toBeVisible();
    await expect(colaTile).toBeVisible();

    // Selecting a real category tab still narrows to just that category —
    // the pre-#2212 behavior, now reached by an explicit tap on the tab
    // rather than being the default.
    await foodTab.click();
    await expect(foodTab).toHaveClass(/active/);
    await expect(butterTile).toBeVisible();
    await expect(colaTile).toBeHidden();

    // Switching tabs is a pure client-side toggle — no request, no reload.
    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(colaTile).toBeVisible();
    await expect(butterTile).toBeHidden();

    // Open search while on Drinks, then search for something that only
    // lives under Food > Dairy. Pre-#2181 this returned nothing ("Butter
    // lives in Food" was the old, intentional dead end); #2181 reverses
    // that: the query now spans every category, so Butter is findable
    // without leaving the Drinks tab, and — since Dairy is a nested
    // subcategory — it's unambiguous which category it's in via Dairy's
    // own always-visible header (unchanged by this card).
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('Butter');
    await expect(butterTile).toBeVisible();
    await expect(page.locator('.category-header', { hasText: 'Dairy' })).toBeVisible();
    await expect(colaTile).toBeHidden(); // doesn't match "Butter"

    // The reverse direction: search for a Drinks item while its own tab
    // isn't active. Cola is a DIRECT Drinks tile (no subcategory of its
    // own), which pre-#2181 never carried its own category header at all
    // (the tab bar communicated it implicitly) — with the tab strip
    // replaced by the search box, that would now be ambiguous, so
    // ut-docs#2181 gives it one too, shown only while a query is active.
    await search.fill('Cola');
    await expect(colaTile).toBeVisible();
    await expect(page.locator('.category-header', { hasText: 'Drinks' })).toBeVisible();
    await expect(butterTile).toBeHidden();

    // ut-docs#2181 review finding F3: with the tablist hidden and TWO panels
    // now visible at once (Food's, for Butter above, and Drinks', for Cola),
    // neither can still claim the WAI-ARIA tabpanel role — that pattern
    // requires exactly one panel, owned by the one selected tab. Both must
    // drop role="tabpanel"/aria-labelledby while a query is active, and get
    // them back the moment the query clears.
    await search.fill('Butter'); // back to a Food-only match, single visible panel
    await expect(page.locator('#cat-panel-cat_food')).toHaveAttribute('role', 'group');
    await expect(page.locator('#cat-panel-cat_food')).not.toHaveAttribute('aria-labelledby', /.+/);
    await search.fill('');
    await expect(page.locator('#cat-panel-cat_food')).toHaveAttribute('role', 'tabpanel');
    await expect(page.locator('#cat-panel-cat_food')).toHaveAttribute('aria-labelledby', 'cat-tab-cat_food');

    // Re-open search on Drinks to continue the flow below.
    await search.fill('Cola');

    // Closing search clears the query (ut-docs#2173) and restores the
    // active-tab-only view — the Drinks-category header disappears again
    // since it's q-gated, not permanent.
    await page.locator('.products-strip-back').click();
    await expect(search).toHaveValue('');
    await expect(colaTile).toBeVisible(); // back to plain Drinks-tab view
    await expect(page.locator('.category-header', { hasText: 'Drinks' })).toBeHidden();

    // A query still narrows within a single category too, same as before.
    await foodTab.click();
    await page.locator('.products-strip-search').click();
    await search.fill('Butter');
    await expect(butterTile).toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeHidden(); // same tab, doesn't match query

    await search.fill('');
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeVisible();

    assertClean();
  });

  test('the All tab shows every category at once, each labelled, without growing the strip row (ut-docs#2212)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    const strip = page.locator('.products-strip');
    const allTab = tabBar.getByRole('tab', { name: 'All' });
    const foodTab = tabBar.getByRole('tab', { name: 'Food' });
    const butterTile = page.locator('.btn-tile', { hasText: 'Butter 250g' }); // Food > Dairy
    const colaTile = page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' }); // Drinks, direct

    // ut-docs#2173's own invariant (same row, same height, never a second
    // row) must still hold once a tab is ADDED to the strip, not just when
    // search toggles within it.
    const beforeBox = await strip.boundingBox();
    expect(beforeBox, 'strip must have a measurable box on first paint').toBeTruthy();

    // Navigate away from the default (Food), then back to All — proving
    // selection, not just the initial default, restores the whole-catalogue
    // view.
    await foodTab.click();
    await expect(colaTile).toBeHidden();
    await allTab.click();
    await expect(allTab).toHaveClass(/active/);
    await expect(tabBar.locator('.tab.active')).toHaveCount(1);

    // Every category's tiles show at once...
    await expect(butterTile).toBeVisible();
    await expect(colaTile).toBeVisible();
    // ...and each carries a visible category header so an item flattened in
    // from a different category is still attributable to it — the same
    // headers ut-docs#2181 already shows during a cross-category search,
    // now also shown for the All tab with no search active.
    await expect(page.locator('.category-header', { hasText: 'Dairy' })).toBeVisible();
    await expect(page.locator('.category-header', { hasText: 'Drinks' })).toBeVisible();

    const afterBox = await strip.boundingBox();
    expect(afterBox, 'strip must have a measurable box with All selected').toBeTruthy();
    expect(
      Math.abs(afterBox!.height - beforeBox!.height),
      `strip height must not change when All is selected (before ${beforeBox!.height}px, after ${afterBox!.height}px)`,
    ).toBeLessThan(1);

    assertClean();
  });

  test('a search matching nothing in the WHOLE catalogue shows exactly one no-matches message (ut-docs#422, scope widened by ut-docs#2181)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    const drinksTab = tabBar.getByRole('tab', { name: 'Drinks' });
    const noMatches = page.locator('#buttons-grid > .empty', { hasText: 'No matching products.' });

    await drinksTab.click();
    await expect(drinksTab).toHaveClass(/active/);
    await expect(noMatches).toBeHidden();

    // ut-docs#2173: open search via the strip's search icon first.
    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('this matches absolutely nothing on the till');
    // Exactly one message for the whole catalogue (ut-docs#2181) — not one
    // per tab panel, since a query can now show several panels at once.
    await expect(noMatches).toHaveCount(1);
    await expect(noMatches).toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' })).toBeHidden();

    // A query matching something in a DIFFERENT category than the one
    // active when search opened must still clear the message — proving the
    // check spans every category, not just Drinks.
    await search.fill('Butter'); // Food > Dairy, not Drinks
    await expect(noMatches).toBeHidden();
    await expect(page.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();

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
