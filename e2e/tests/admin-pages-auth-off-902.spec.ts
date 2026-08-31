import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#902: country-settings/kitchen-stations/promotions/tables/
// translations all shared the same page-local requireManager closure built
// directly on auth.FromContext(...).IsManager(), which -- like every page
// ut-docs#901 fixed before it (locations/registers) -- fails closed
// PERMANENTLY under UT_AUTH=off (this suite's default project), because
// auth.FromContext never has a value set when internal/pages/init.go skips
// auth.Middleware entirely. Migrated onto canPerform(d, r, "settings"),
// which has the UT_AUTH=off bypass, exactly like #901's fix and every other
// admin page. tables_page.go already had real e2e coverage
// (tables-keyboard-reposition-826.spec.ts, moved onto this project by this
// same change) and locations/registers were covered by #901's own spec, so
// this file is deliberately just a minimal reachability smoke test for the
// remaining four -- not a full CRUD walk like #901's, per this card's own
// non-goals.
test.describe('admin pages reachable under UT_AUTH=off (ut-docs#902)', () => {
  test('/country-settings renders', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings');
    // The permanent-403 regression: a failed GET here renders a plain-text
    // 403 body (common.LocalizedError -> http.Error), which has no <h1> --
    // not the country-settings table.
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('table.table').first()).toBeVisible();
    assertClean();
  });

  test('/kitchen-stations renders', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/kitchen-stations');
    await expect(page.locator('h1')).toBeVisible();
    // No table.table assertion here: unlike the other three pages, this
    // template renders a "none configured" message instead of the table
    // when the shop has zero stations (web/ui/pages/kitchen_stations.html)
    // -- the demo e2e till seeds none, so the .card.users-list container
    // (present on both branches) is the one true reachability signal.
    await expect(page.locator('.card.users-list')).toBeVisible();
    assertClean();
  });

  test('/promotions renders', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/promotions');
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('table.table').first()).toBeVisible();
    assertClean();
  });

  test('/translations renders', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/translations?edit_locale=en');
    await expect(page.locator('h1')).toBeVisible();
    assertClean();
  });
});
