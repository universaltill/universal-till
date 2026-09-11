import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2090: product-owner report — pressing one of the catalog panel's
// top actions from /items lost the left rail entirely (a plain full-page
// <a href>, not an htmx in-panel swap), and the destination's only way
// back pointed at bare /catalog, which also has no rail — a one-way door
// out of the /items shell on kiosk hardware with no browser chrome/Back.
//
// Modifiers and Option sets ARE /items rail sections (itemsnav.Resolve's
// five rows) — their handlers already answer an htmx fragment request with
// the OOB rail swap (ut-docs#1950). #2090's own fix gave catalog.html's
// top-row Modifiers/Option-sets buttons the same hx-get/hx-target/
// hx-push-url the rail's own rows already carried — but ut-docs#2092
// (landed right after, same day) removed those two top-row buttons
// entirely: they duplicated the rail exactly, and the rail is reachable
// from /items regardless, so the "second path" #2090 was fixing no longer
// exists to fix. What #2090 actually locked in — the rail survives
// navigating to Modifiers/Option-sets from the /items shell, and each
// destination's back-link returns to the shell with the rail visible — is
// still real and still worth pinning; only the trigger changes, from the
// (now-removed) top-row button to the rail's own row, which was always
// the primary path anyway. Import and Tax codes are NOT rail sections;
// fixing those to also stay in-panel is a separate, larger follow-up
// (ut-docs#2095) — out of scope here.
//
// Uses expect(page).toHaveURL(...) throughout rather than a one-shot
// expect(page.url())...: hx-push-url runs asynchronously after htmx's DOM
// swap, so a non-retrying assertion taken right after the click can race
// it and read the URL before the push lands even though the swap itself
// already completed correctly.
test.describe('/items shell: navigating to Modifiers/Option sets keeps the rail (ut-docs#2090, trigger updated for ut-docs#2092)', () => {
  test('Modifiers keeps the rail, and its back-link returns to Catalog in the shell', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');

    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');

    // ut-docs#2092 removed catalog.html's own top-row Modifiers button
    // (#catalog-modifiers-btn) — it duplicated this exact rail row. The
    // rail row is the only path now, and was always the rail-preserving
    // one #2090 built its OOB-swap fix against.
    await page.locator('.items-row[href="/modifiers"]').click();

    // A real htmx in-panel swap, not a full navigation away from the shell.
    await expect(page).toHaveURL(/\/modifiers$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/modifiers');
    await expect(page.locator('#items-panel h1')).toHaveText('Customization options');

    // The back-link inside Modifiers returns to Catalog, in-panel, rail
    // still present — not bare /catalog (which has none).
    await page.locator('#items-panel .page-head a', { hasText: '←' }).click();
    await expect(page).toHaveURL(/\/catalog$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    await expect(page.locator('#items-panel h1')).toHaveText('Catalog');
    assertClean();
  });

  test('Option sets keeps the rail, and its back-link returns to Catalog in the shell', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await expect(page.locator('#items-rail')).toBeVisible();

    // ut-docs#2092 removed catalog.html's own top-row Option-sets button
    // (#catalog-option-sets-btn) — it duplicated this exact rail row.
    await page.locator('.items-row[href="/catalog/option-sets"]').click();

    await expect(page).toHaveURL(/\/catalog\/option-sets$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog/option-sets');

    await page.locator('#items-panel .page-head a', { hasText: '←' }).click();
    await expect(page).toHaveURL(/\/catalog$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    assertClean();
  });

  // AC #3 (ut-docs#1950's own guarantee, re-asserted here): a bare deep
  // link to /modifiers or /catalog/option-sets still renders the complete
  // standalone page — no rail, and the button/back-link stay plain
  // navigation (no #items-panel on the page for hx-target to hit).
  test('a direct visit to /modifiers still renders the standalone page, no rail', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/modifiers');
    await expect(page.locator('#items-rail')).toHaveCount(0);
    await expect(page.locator('.nav')).toBeVisible();
    const backLink = page.locator('.page-head a', { hasText: '←' });
    await expect(backLink).toHaveAttribute('href', '/items');
    assertClean();
  });
});
