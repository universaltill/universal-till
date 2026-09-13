import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2167: product-owner report — /country-settings' "Show all
// countries" / "Show only my country" were plain <a href> anchors, so
// following one from inside /admin's two-pane shell (ut-docs#2116) was a
// full navigation: the admin tree and its selection state were torn down to
// reload the one page already showing in the panel. They are in-panel htmx
// chips now (.filter-chips, the ut-docs#2119 chip vocabulary), targeting
// their own #country-settings-view subtree via hx-select.
//
// The mechanism these tests actually pin, and why each half is needed:
//  - the shell half asserts #admin-tree SURVIVES the toggle and keeps
//    /country-settings marked current. That is the regression; a Go
//    handler test cannot see it, because the tree's survival is a property
//    of what htmx does to the DOM, not of what the handler writes.
//  - the standalone half asserts the same control still works on a bare
//    GET, where there is no #admin-panel and no tree at all — the case
//    where hx-select has to pull one div out of a response that also
//    carries the page-head, with no shell around it.
//
// One thing this file deliberately does NOT claim to cover: the server-side
// admin-tree-suppression guard (internal/pages/admin_page.go's
// isAdminInlineSwap, ut-docs#2178 — replaced the old isAdminPanelSwap).
// Reverting that guard leaves both tests below GREEN — htmx 1.9 discards an
// out-of-band fragment with no matching target silently, with no console
// error for watchConsole to catch and nothing inserted into the DOM. That
// guard's real coverage is the Go handler test
// TestCountrySettings_FragmentOmitsAdminTreeOnlyWhenInlineSwapMarked. The
// #admin-tree count assertion below is kept as a cheap tripwire for a
// future htmx upgrade changing that behaviour, not as proof of the guard.
//
// Uses expect(page).toHaveURL(...) rather than a one-shot expect(page.url()):
// hx-push-url lands asynchronously after the DOM swap, so a non-retrying
// assertion taken right after the click can race it — the same caveat
// items-shell-catalog-top-actions-rail-2090.spec.ts records.
test.describe('/country-settings scope filter stays in-panel (ut-docs#2167)', () => {
  test('toggling from inside the /admin shell keeps the tree and its selection', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/admin');
    await expect(page.locator('#admin-tree')).toBeVisible();

    await page.locator('.items-row[href="/country-settings"]').click();
    await expect(page).toHaveURL(/\/country-settings$/);
    await expect(page.locator('#admin-tree')).toBeVisible();
    await expect(page.locator('#admin-tree .items-row.is-current')).toHaveAttribute('href', '/country-settings');

    const scope = page.locator('#country-settings-view .filter-chips');
    await expect(scope).toBeVisible();
    const showAll = scope.locator('.chip').nth(1);
    const showMine = scope.locator('.chip').nth(0);
    await expect(showMine).toHaveAttribute('aria-pressed', 'true');
    await expect(showAll).toHaveAttribute('aria-pressed', 'false');

    await showAll.click();

    // The regression: before this card, this click left the shell entirely.
    await expect(page).toHaveURL(/\/country-settings\?all=1$/);
    await expect(page.locator('#admin-tree')).toBeVisible();
    await expect(page.locator('#admin-tree .items-row.is-current')).toHaveAttribute('href', '/country-settings');
    await expect(page.locator('#country-settings-view .filter-chips .chip').nth(1))
      .toHaveAttribute('aria-pressed', 'true');
    // The all-countries view really did load, not just the chip flip.
    expect(await page.locator('#country-settings-view table.table tbody tr').count()).toBeGreaterThan(1);
    // ut-docs#2178 AC: a screen reader gets a spoken confirmation of the
    // toggle — #country-settings-status sits OUTSIDE #country-settings-view
    // (survives its outerHTML swap) and is updated to the now-pressed
    // chip's own label by the page's htmx:afterSwap listener.
    await expect(page.locator('#country-settings-status')).toHaveText(await showAll.innerText());

    // And back again, still in the shell.
    await page.locator('#country-settings-view .filter-chips .chip').nth(0).click();
    await expect(page).toHaveURL(/\/country-settings$/);
    await expect(page.locator('#admin-tree')).toBeVisible();
    assertClean();
  });

  test('the same control works on the standalone page, with no tree to swap into', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings');
    await expect(page.locator('#admin-tree')).toHaveCount(0);

    const chips = page.locator('#country-settings-view .filter-chips .chip');
    await chips.nth(1).click();

    await expect(page).toHaveURL(/\/country-settings\?all=1$/);
    await expect(page.locator('#country-settings-view')).toBeVisible();
    await expect(chips.nth(1)).toHaveAttribute('aria-pressed', 'true');
    // Still exactly one wrapper — an hx-select/hx-swap mismatch would nest
    // a second #country-settings-view inside the first, or replace it with
    // a whole document.
    await expect(page.locator('#country-settings-view')).toHaveCount(1);
    await expect(page.locator('#country-settings-view table.table')).toBeVisible();
    // The out-of-band admin tree must not have leaked into a page that has
    // no shell to receive it (internal/pages/admin_page.go's
    // isAdminInlineSwap, ut-docs#2178). Asserted AFTER the swap, not only
    // before it: htmx 1.9 does not raise a console error for an oob
    // fragment with no matching target, so watchConsole alone cannot see
    // this — verified by reverting the guard and watching this file still
    // pass without this line.
    await expect(page.locator('#admin-tree')).toHaveCount(0);
    assertClean();
  });
});
