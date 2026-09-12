import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2095 (split from #2090): Import and Tax codes are NOT /items rail
// sections (itemsnav.Resolve's five rows are Catalog/Categories/Inventory/
// Modifiers/Option sets only), so unlike Modifiers/Option sets (#2090) they
// must NOT swap #items-panel. Instead, from inside the /items shell they now
// open as a closable full-screen <dialog> overlay (the #barcode-backfill-modal
// pattern) floating ABOVE the shell, so the left rail stays visible/present
// behind them the whole time. This spec locks in: the dialog opens, the rail
// survives behind it, closing the dialog returns to /items with Catalog still
// selected and the rail intact (no URL push at all — never navigated away),
// and a direct/bare GET of either route still renders the full standalone
// page with its own plain back-link, unchanged from before this card.
test.describe('/items shell: Import and Tax codes open as a dialog, not a rail swap (ut-docs#2095)', () => {
  test('Import opens in a dialog above the rail, and closing it returns to /items/Catalog', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');

    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');

    await page.locator('#catalog-import-btn').click();

    const dialog = page.locator('#import-modal');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('h1')).toHaveText('Import catalog');

    // The rail is still present/visible in the DOM BEHIND the dialog --
    // this is the whole point of the dialog-overlay approach over the
    // Modifiers/Option-sets in-panel swap, which would have replaced it.
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    // Never navigated: opening the dialog does not push a new URL.
    await expect(page).toHaveURL(/\/items$/);

    // Close via the dialog's own back-link, now acting as a close button.
    await dialog.locator('.page-head a', { hasText: '←' }).click();
    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/\/items$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    assertClean();
  });

  test('Tax codes opens in a dialog above the rail, and closing it returns to /items/Catalog', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await expect(page.locator('#items-rail')).toBeVisible();

    await page.locator('#catalog-taxcodes-btn').click();

    const dialog = page.locator('#tax-codes-modal');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('h1')).toContainText('Tax codes');

    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    await expect(page).toHaveURL(/\/items$/);

    await dialog.locator('.page-head a', { hasText: '←' }).click();
    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/\/items$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    assertClean();
  });

  // AC3: a bare/direct visit to either route (no htmx) still renders the
  // complete standalone page -- no rail, no dialog wrapper, and the
  // back-link stays a plain navigation to /items (there is no dialog to
  // close standalone). Two other pages added by #2090 still link to these
  // as standalone destinations, so this is the single most important
  // regression to protect.
  test('a direct visit to /import still renders the standalone page, no rail, plain back-link', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/import');
    await expect(page.locator('#items-rail')).toHaveCount(0);
    await expect(page.locator('.nav')).toBeVisible();
    await expect(page.locator('h1')).toHaveText('Import catalog');
    const backLink = page.locator('.page-head a', { hasText: '←' });
    await expect(backLink).toHaveAttribute('href', '/items');
    await backLink.click();
    await expect(page).toHaveURL(/\/items$/);
    assertClean();
  });

  test('a direct visit to /catalog/tax-codes still renders the standalone page, no rail, plain back-link', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog/tax-codes');
    await expect(page.locator('#items-rail')).toHaveCount(0);
    await expect(page.locator('.nav')).toBeVisible();
    await expect(page.locator('h1')).toContainText('Tax codes');
    const backLink = page.locator('.page-head a', { hasText: '←' });
    await expect(backLink).toHaveAttribute('href', '/items');
    await backLink.click();
    await expect(page).toHaveURL(/\/items$/);
    assertClean();
  });

  // Independent review findings F1/F2 (fixed in this same commit): both
  // dialogs are opened via .show(), NOT .showModal() -- a showModal()
  // dialog enters the browser's top layer and makes everything outside it
  // (including #osk, the custom on-screen keyboard kiosk hardware depends
  // on entirely) inert and unreachable, the exact #1385 bug. `:modal` is
  // the CSS pseudo-class that ONLY matches a showModal()'d dialog -- a
  // .show()'d one never matches it, regardless of its [open] attribute --
  // so asserting it's false is a direct, DOM-level pin against a future
  // regression back to showModal(), not just an indirect behavioural proxy.
  test('the tax-codes dialog opens non-modal (.show(), not .showModal()) so the on-screen keyboard stays reachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await page.locator('#catalog-taxcodes-btn').click();
    const dialog = page.locator('#tax-codes-modal');
    await expect(dialog).toBeVisible();
    // The direct, DOM-level pin: `:modal` ONLY matches a showModal()'d
    // dialog, regardless of its [open] attribute -- a .show()'d one never
    // matches it. This is what a future regression back to showModal()
    // would flip.
    const isModal = await dialog.evaluate((el) => (el as HTMLDialogElement).matches(':modal'));
    expect(isModal).toBe(false);
    // Behavioural proof, not just the DOM pin above: osk.js shows the
    // keyboard on a real CLICK of an OSK-able field (not programmatic
    // focus, see osk.js's own comment on why) -- drive that for real and
    // confirm a key press actually reaches the field. A showModal() dialog
    // would make #osk (appended to <body>, never re-parented into the
    // dialog) inert -- this is the exact #1385 failure mode.
    const nameInput = dialog.locator('#tax-code-name');
    await nameInput.click();
    const osk = page.locator('#osk');
    await expect(osk).toBeVisible();
    // Any plain letter key (.osk-key, excluding the wider .osk-fn function
    // keys like Backspace/Enter/Shift/space) -- don't depend on which
    // specific character it is, just that a real key press actually
    // reaches the field instead of landing on an inert page underneath it.
    await osk.locator('.osk-key:not(.osk-fn)').first().click();
    await expect(nameInput).not.toHaveValue('');
    assertClean();
  });

  // Independent review finding F2: hx-on::after-request must not open the
  // dialog on a non-2xx response -- an unguarded .show() there would trap
  // the operator in an empty/erroring modal with no close control reachable
  // on a touch-only till (CLAUDE.md §10, ut-docs#1999).
  test('a failed request does not open the tax-codes dialog', async ({ page }) => {
    const assertClean = watchConsole(page, /boom|500/);
    await page.goto('/items');
    await page.route('**/catalog/tax-codes', (route) => route.fulfill({ status: 500, body: 'boom' }));
    await page.locator('#catalog-taxcodes-btn').click();
    await page.waitForTimeout(300); // let the (failed) htmx request settle
    await expect(page.locator('#tax-codes-modal')).toBeHidden();
    await expect(page.locator('#items-rail')).toBeVisible();
    assertClean();
  });
});
