import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2162: independent review of ut-docs#2116 found that neither the
// /items nor the /admin two-pane shell (ut-docs#1950 / ut-docs#2116) ever
// updates document.title after an in-panel htmx swap — hx-push-url moves
// the URL, but the browser tab keeps showing whichever section/destination
// was current when the shell itself first loaded.
//
// Root cause: web/ui/layouts/base.html's own <title> tag only renders on a
// full-page response; a swap response is a bare fragment
// (httpx.RenderContentFragment) that never carried title information at
// all. Fixed by a response header (X-UT-Page-Title, percent-encoded) set
// whenever the swapped fragment's own template data carries a "title",
// read back by a single, shared, target-id-scoped htmx:afterSwap listener
// in web/public/app.js (see that listener's own comment for why it's
// scoped to an explicit allowlist rather than reacting to every response
// that happens to carry the header).
//
// Each shell starts with its own bare-GET title ("Items"/"Administration")
// per the existing, unchanged behavior for the FIRST section embedded on
// arrival — this card's scope is specifically the title going stale on a
// SUBSEQUENT swap, so the assertions below click somewhere else first.
test.describe('/items and /admin shells keep document.title in sync with the swapped panel (ut-docs#2162)', () => {
  test('/items: swapping the rail to Inventory updates the tab title', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    expect(page.url()).not.toContain('/inventory');

    await page.locator('.items-row[href="/inventory"]').click();

    await expect(page).toHaveURL(/\/inventory$/);
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/inventory');
    await expect.poll(() => page.title()).toBe('Inventory');
    assertClean();
  });

  test('/admin: swapping the tree to Translations updates the tab title', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/admin');
    await expect(page.locator('#admin-tree')).toBeVisible();

    await page.locator('.items-row[href="/translations"]').click();

    await expect(page).toHaveURL(/\/translations$/);
    await expect(page.locator('#admin-tree .items-row.is-current')).toHaveAttribute('href', '/translations');
    await expect.poll(() => page.title()).toBe('Translations');
    assertClean();
  });

  test('/items: swapping the rail back to Catalog (the shell\'s own default section, rendered via a different code path — RenderWith, not RenderContentFragment) updates the tab title', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await page.locator('.items-row[href="/inventory"]').click();
    await expect(page).toHaveURL(/\/inventory$/);
    await expect.poll(() => page.title()).toBe('Inventory');

    await page.locator('.items-row[href="/catalog"]').click();

    await expect(page).toHaveURL(/\/catalog$/);
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    await expect.poll(() => page.title()).toBe('Catalog');
    assertClean();
  });

  // Review finding (2026-09-12): every title above is a single word, so
  // none of them would have caught the real bug an independent review
  // found in the first draft — url.QueryEscape (Go) encodes a space as
  // "+", which decodeURIComponent (JS) does NOT decode back to a space,
  // so a genuinely multi-word title (most of the real ones — "Country
  // settings", "Fiscal register", "Tax codes"...) rendered a literal "+"
  // in the tab instead of a space. Fixed with url.PathEscape/
  // PathUnescape's %20 encoding instead. This test exists specifically
  // so that regression can never come back unnoticed.
  test('/admin: swapping the tree to a multi-word destination (Country settings) renders a real space, not "+"', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/admin');
    await expect(page.locator('#admin-tree')).toBeVisible();

    await page.locator('.items-row[href="/country-settings"]').click();

    await expect(page).toHaveURL(/\/country-settings$/);
    await expect(page.locator('#admin-tree .items-row.is-current')).toHaveAttribute('href', '/country-settings');
    await expect.poll(() => page.title()).toBe('Country settings');
    assertClean();
  });

  test('a bare-GET deep link to a section still gets its own correct title (unaffected by the swap fix)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    await expect.poll(() => page.title()).toBe('Inventory');
    assertClean();
  });
});
