import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2119 — category filter on /catalog and /inventory. Drives the
// real demo-seeded catalog (internal/data/seeddata/demo_catalogue.sql)
// rather than importing fixture data, same convention as
// catalog-category-brand-select-1430.spec.ts: "Food" (cat_food) and
// "Drinks" (cat_drink) are real top-level categories, and Food genuinely
// has real child categories in the seed (Bakery/Dairy/Frozen/Snacks) with
// some items filed directly under Food (e.g. "Kellogg's Cornflakes 500g")
// and others under a Food child (e.g. "White Bread Loaf" under Bakery) —
// so the parent-includes-children case is exercised against real data,
// no synthetic seeding needed.

test.describe('catalog category filter (ut-docs#2119)', () => {
  test('chips render, narrow the grid, include a child category under its parent, compose with search, and All categories clears it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const filterRow = page.locator('#catalog-category-filter');
    await expect(filterRow).toBeVisible();
    await expect(filterRow).toHaveAttribute('role', 'group');

    const allChip = filterRow.locator('[data-cat-all]');
    const foodChip = filterRow.locator('[data-cat-id="cat_food"]');
    const drinksChip = filterRow.locator('[data-cat-id="cat_drink"]');
    await expect(allChip).toBeVisible();
    await expect(foodChip).toBeVisible();
    await expect(drinksChip).toBeVisible();
    // Only top-level categories get their own chip — Bakery/Dairy/Frozen/
    // Snacks (children of Food) must not appear as separate chips.
    await expect(filterRow.locator('[data-cat-id="cat_bakery"]')).toHaveCount(0);

    // Starting state: nothing selected, "All categories" reads as pressed.
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(drinksChip).toHaveAttribute('aria-pressed', 'false');

    const pepsi = page.locator('.catalog-row', { hasText: 'Pepsi Can 330ml' });
    const bread = page.locator('.catalog-row', { hasText: 'White Bread Loaf' });
    const cornflakes = page.locator('.catalog-row', { hasText: "Kellogg's Cornflakes 500g" });
    await expect(pepsi).toBeVisible();
    await expect(bread).toBeVisible();
    await expect(cornflakes).toBeVisible();

    // Tapping "Drinks" (no children) narrows to drinks only.
    await drinksChip.click();
    await expect(drinksChip).toHaveAttribute('aria-pressed', 'true');
    await expect(allChip).toHaveAttribute('aria-pressed', 'false');
    await expect(pepsi).toBeVisible();
    await expect(bread).toBeHidden();
    await expect(cornflakes).toBeHidden();

    // Tapping "Food" instead (toggle Drinks off, Food on): includes BOTH an
    // item filed directly under Food (Cornflakes) AND one filed under a
    // CHILD of Food, Bakery (White Bread Loaf) — the parent-includes-
    // children behaviour, against real nested seed data.
    await drinksChip.click();
    await foodChip.click();
    await expect(foodChip).toHaveAttribute('aria-pressed', 'true');
    await expect(pepsi).toBeHidden();
    await expect(bread).toBeVisible();
    await expect(cornflakes).toBeVisible();

    // Compose with search (AND, not replace): narrows further within the
    // already-selected category.
    const search = page.locator('#catalog-search');
    await search.fill('Bread');
    await expect(bread).toBeVisible();
    await expect(cornflakes).toBeHidden();

    // Search text that matches nothing under the current category filter:
    // the distinct "no matches" state, not the true empty-catalog state.
    await search.fill('Zzzznomatchxyz');
    await expect(page.locator('#catalog-no-matches')).toBeVisible();
    await expect(page.locator('#catalog-empty-row')).toBeHidden();

    // "All categories" clears the category filter (search still applies).
    await search.fill('');
    await allChip.click();
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(foodChip).toHaveAttribute('aria-pressed', 'false');
    await expect(pepsi).toBeVisible();
    await expect(bread).toBeVisible();
    await expect(cornflakes).toBeVisible();
    await expect(page.locator('#catalog-no-matches')).toBeHidden();

    assertClean();
  });
});

test.describe('inventory category filter (ut-docs#2119)', () => {
  test('chips render and narrow the stock table the same way', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');

    const filterRow = page.locator('#inventory-category-filter');
    await expect(filterRow).toBeVisible();
    const allChip = filterRow.locator('[data-cat-all]');
    const drinksChip = filterRow.locator('[data-cat-id="cat_drink"]');
    const foodChip = filterRow.locator('[data-cat-id="cat_food"]');
    await expect(allChip).toBeVisible();
    await expect(drinksChip).toBeVisible();
    await expect(foodChip).toBeVisible();

    // Scoped to the item-level row specifically (data-variant="") — Pepsi
    // also has its own separately-tracked variant row ("— Pack of 6"),
    // additive per ADR-0043 (ut-docs#2082), which a bare hasText match
    // would ambiguously also match.
    const pepsiRow = page.locator('#stock-table .stock-row[data-item="itm002"][data-variant=""]');
    const breadRow = page.locator('#stock-table .stock-row', { hasText: 'White Bread Loaf' });
    await expect(pepsiRow).toBeVisible();
    await expect(breadRow).toBeVisible();

    await drinksChip.click();
    await expect(pepsiRow).toBeVisible();
    await expect(breadRow).toBeHidden();

    // Search text matching nothing under the active category filter shows
    // the distinct "no matches" row, not the true "no stock recorded yet" one.
    await page.locator('#stock-search').fill('Zzzznomatchxyz');
    await expect(page.locator('#stock-no-matches')).toBeVisible();

    await page.locator('#stock-search').fill('');
    await allChip.click();
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(pepsiRow).toBeVisible();
    await expect(breadRow).toBeVisible();
    await expect(page.locator('#stock-no-matches')).toBeHidden();

    assertClean();
  });
});

test.describe('category filter still binds after an /items rail swap (ut-docs#2119 review finding F1)', () => {
  // Regression coverage for a real defect the review pass found: both
  // pages originally gated CategoryFilter.bind() on a plain
  // `DOMContentLoaded` listener, which never fires again once that event
  // has already happened — true on a full page load (every OTHER test in
  // this file uses page.goto() directly, which is why they never caught
  // this), but NOT true when a page is reached via the /items rail's htmx
  // fragment swap (hx-target="#items-panel", htmx re-executing the
  // swapped-in <script> block): the surrounding document's
  // DOMContentLoaded fired long before the swap, so the listener sat
  // registered forever and the chips rendered but silently never bound.
  test('Inventory chips respond after navigating there via the rail, not just on a direct load', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items'); // Catalog loads by default, one full page load
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');

    // Swap to Inventory via the rail — the htmx path this defect only
    // showed up on, never a direct page.goto('/inventory').
    await page.locator('.items-row[href="/inventory"]').click();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/inventory');

    const filterRow = page.locator('#inventory-category-filter');
    await expect(filterRow).toBeVisible();
    const drinksChip = filterRow.locator('[data-cat-id="cat_drink"]');
    const pepsiRow = page.locator('#stock-table .stock-row[data-item="itm002"][data-variant=""]');
    const breadRow = page.locator('#stock-table .stock-row', { hasText: 'White Bread Loaf' });
    await expect(pepsiRow).toBeVisible();
    await expect(breadRow).toBeVisible();

    await drinksChip.click();
    // Before the F1 fix: aria-pressed never flips and both rows stay
    // visible — bind() was never called on this path.
    await expect(drinksChip).toHaveAttribute('aria-pressed', 'true');
    await expect(pepsiRow).toBeVisible();
    await expect(breadRow).toBeHidden();

    assertClean();
  });

  test('Catalog chips respond after navigating BACK to it via the rail, not just on the initial load', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items'); // Catalog loads by default (initial load DOES work pre-fix)
    // Swap away and back — the round trip is what exercises the fragment
    // re-render path a bare initial load never does.
    await page.locator('.items-row[href="/inventory"]').click();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/inventory');
    await page.locator('.items-row[href="/catalog"]').click();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');

    const filterRow = page.locator('#catalog-category-filter');
    await expect(filterRow).toBeVisible();
    const drinksChip = filterRow.locator('[data-cat-id="cat_drink"]');
    const pepsi = page.locator('.catalog-row', { hasText: 'Pepsi Can 330ml' });
    const bread = page.locator('.catalog-row', { hasText: 'White Bread Loaf' });
    await expect(pepsi).toBeVisible();
    await expect(bread).toBeVisible();

    await drinksChip.click();
    await expect(drinksChip).toHaveAttribute('aria-pressed', 'true');
    await expect(pepsi).toBeVisible();
    await expect(bread).toBeHidden();

    assertClean();
  });
});

test.describe('category-filter.js pure functions (ut-docs#2119)', () => {
  // No JS unit-test runner in this codebase (only Playwright, per
  // e2e/package.json) — exercised here directly against the real shipped
  // file (loaded by base.html on every page) instead of a reimplementation,
  // covering deep nesting (grandchild categories) that the demo seed data
  // does not itself go more than one level deep for.
  test('expand() walks multi-level nesting and matches() treats an empty selection as "show everything"', async ({ page }) => {
    await page.goto('/catalog');
    await page.waitForFunction(() => !!(window as any).CategoryFilter);

    const result = await page.evaluate(() => {
      const nodes = [
        { id: 'root', name: 'Root', parentId: '' },
        { id: 'child', name: 'Child', parentId: 'root' },
        { id: 'grandchild', name: 'Grandchild', parentId: 'child' },
        { id: 'unrelated', name: 'Unrelated', parentId: '' },
      ];
      const CF = (window as any).CategoryFilter;
      const expanded = CF.expand(['root'], nodes);
      return {
        expandedIds: Array.from(expanded).sort(),
        matchesGrandchild: CF.matches('grandchild', expanded),
        matchesUnrelated: CF.matches('unrelated', expanded),
        emptySelectionMatchesAnything: CF.matches('literally-anything', CF.expand([], nodes)),
      };
    });

    expect(result.expandedIds).toEqual(['child', 'grandchild', 'root']);
    expect(result.matchesGrandchild).toBe(true);
    expect(result.matchesUnrelated).toBe(false);
    expect(result.emptySelectionMatchesAnything).toBe(true);
  });
});
