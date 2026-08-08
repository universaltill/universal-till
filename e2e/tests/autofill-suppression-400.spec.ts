import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#400: the browser's own field-history dropdown was covering the
// barcode field mid-scan, offering values typed into unrelated fields — and
// on a SHARED till, leaking one operator's history to the next. Fixed
// centrally in web/public/app.js (a MutationObserver-backed sweep), not per
// template, so this asserts the RUNTIME behaviour on real rendered pages —
// a static grep of the templates would miss the point of a JS-driven fix.

test.describe('browser autofill suppression (ut-docs#400)', () => {
  test('sale-screen scan-row inputs get suppressed, everywhere on the page', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    // The reported field itself: the barcode/code input in .scan-row.
    const code = page.locator('.scan-row input[name="code"]');
    await expect(code).toHaveAttribute('autocomplete', /^off-/);
    await expect(code).toHaveAttribute('autocapitalize', 'off');
    await expect(code).toHaveAttribute('autocorrect', 'off');
    await expect(code).toHaveAttribute('spellcheck', 'false');

    // A second input with no autocomplete declared at all (the qty field in
    // the same row) — confirms this isn't special-cased to just the one
    // reported field.
    const qty = page.locator('.scan-row input[name="qty"]');
    await expect(qty).toHaveAttribute('autocomplete', /^off-/);

    // An input identified only by id, no name — the fallback key.
    const pfandAmount = page.locator('#pfand-amount');
    await expect(pfandAmount).toHaveAttribute('autocomplete', /^off-pfand-amount/);

    assertClean();
  });

  test('an input that already declares its own autocomplete is left alone', async ({ page }) => {
    await page.goto('/');

    // web/ui/pages/index.html's pfand manager_pin field ships with an
    // explicit autocomplete="off" already in the template. The sweep must
    // not rewrite an existing declaration into the off-<key> form — that
    // would suggest it clobbers deliberate per-field choices, which is
    // exactly what pin.html's current-password/new-password tokens need it
    // NOT to do.
    const managerPin = page.locator('input[name="manager_pin"]').first();
    await expect(managerPin).toHaveAttribute('autocomplete', 'off');
    // Still gets the unconditional three, since those never conflict with a
    // deliberate autocomplete choice.
    await expect(managerPin).toHaveAttribute('autocapitalize', 'off');
    await expect(managerPin).toHaveAttribute('spellcheck', 'false');
  });

  test('an input added later by an htmx swap is caught too, not just the initial DOM', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    // The basket panel loads via hx-get on page load (hx-swap="outerHTML"),
    // i.e. strictly AFTER the sweep's own DOMContentLoaded pass already ran
    // — this is the htmx:afterSwap path, not the initial load path.
    await page.request.post('/api/pos/reset');
    const codeInput = page.locator('.scan-row input[name="code"]');
    await codeInput.fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    const qtyInBasket = page.locator('#basket .qty-input').first();
    await expect(qtyInBasket).toHaveAttribute('autocomplete', /^off-/);
    await expect(qtyInBasket).toHaveAttribute('spellcheck', 'false');

    await page.request.post('/api/pos/reset');
    assertClean();
  });
});
