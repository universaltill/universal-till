import { test, expect } from './fixtures';

// ut-docs#1625 (found reviewing #1542): #payment-overlay opens non-modally
// (.show(), ut-docs#1385 — the on-screen keyboard must stay usable while
// it's open), so nothing outside it becomes inert/aria-hidden. #1542 added
// duplicate Hold Sale/New Sale buttons INSIDE the overlay so they're
// reachable while it covers the originals in .tender-default-footer — but
// unlike the breakpoint-gated phone-width duplicate (kiosk-checkout-start-
// phone, only one copy ever in the a11y tree at a time), these two copies
// are visible and interactive SIMULTANEOUSLY with the originals. A
// screen-reader/keyboard user tabbing through with the overlay open would
// land on two identically-named "Hold Sale" controls and two identically-
// named "New Sale" controls with no way to tell which is which.
//
// Fix: the in-overlay copies carry their own distinguishing aria-label
// (visible text unchanged) rather than making .tender-default-footer inert
// — inert-ing it would have broken the already-legitimate wide-viewport
// path where the originals aren't covered at all and are driven directly
// (new-sale-closes-payment-overlay-1386.spec.ts's "Hold Sale closes an open
// payment overlay" test, at 1920x1080).
test.describe('payment overlay duplicate controls have distinguishing accessible names (ut-docs#1625)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('Hold Sale and New Sale each have exactly one plainly-named control while the overlay is open, and the in-overlay copies are named distinctly', async ({ page }) => {
    // 1024x600 (this product's own kiosk floor): both originals sit
    // genuinely covered by the overlay here (payment-overlay-footer-
    // reachable-1542.spec.ts), the case this ambiguity is worst for —
    // but the fix must hold regardless of coverage, since accessible-name
    // collision isn't a geometry problem.
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    // Exactly one control is named plainly "Hold Sale" (the original,
    // plain-text label) and exactly one is named plainly "🛒 New Sale" (the
    // original — its visible 🛒 icon is unwrapped text, not aria-hidden, so
    // it's part of the computed accessible name too) — the in-overlay
    // copies must NOT also match these exact accessible names, or a
    // screen-reader user tabbing through still can't tell the pair apart.
    // `exact: true` matters: without it Playwright's substring matching
    // would count the in-overlay copy too (its name still contains "Hold
    // Sale"/"New Sale" as a substring), defeating the point of this
    // assertion.
    await expect(page.getByRole('button', { name: 'Hold Sale', exact: true })).toHaveCount(1);
    await expect(page.getByRole('button', { name: '🛒 New Sale', exact: true })).toHaveCount(1);

    // The in-overlay copies are real, visible, distinctly-named controls —
    // not just absent from the plain-name query above by accident (e.g. a
    // typo'd label that also fails to match anything).
    const holdCopy = page.getByTestId('payment-overlay-hold');
    const newSaleCopy = page.getByTestId('payment-overlay-new-sale');
    await expect(holdCopy).toBeVisible();
    await expect(newSaleCopy).toBeVisible();
    await expect(holdCopy).toHaveAccessibleName('Hold Sale (Tender panel)');
    await expect(newSaleCopy).toHaveAccessibleName('New Sale (Tender panel)');

    // Both copies stay real, fully wired controls under the new label —
    // the fix must not have made them decorative or non-interactive.
    await holdCopy.click();
    await expect(page.locator('#hold-modal')).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      page.locator('#hold-modal button[type=submit]').click(),
    ]);
    await expect(page.locator('#hold-modal')).toBeHidden();
  });

  // The originals must stay fully reachable/clickable while the overlay is
  // open — this is the regression this fix must not cause (ut-docs#1386,
  // driven directly here rather than just re-asserted by reference): a
  // blanket `inert` on `.tender-default-footer` would break this at wide
  // viewports where the overlay never covers the originals at all.
  test('the ORIGINAL Hold Sale button is still clickable while the overlay is open at a wide viewport', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    const originalHold = page.locator('.tender-default-footer button', { hasText: 'Hold Sale' });
    await expect(originalHold).toBeEnabled();
    await originalHold.click(); // no force: must be a genuinely landable, non-inert click
    await expect(page.locator('#hold-modal')).toBeVisible();
  });
});
