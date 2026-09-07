import { test, expect } from './fixtures';

// ut-docs#1386: .tender-default-footer's New Sale and Hold Sale sit outside
// #payment-overlay. Since ut-docs#1385 made the overlay non-modal, both are
// real, reachable hit targets while the overlay is open — and both reset
// the live basket server-side (New Sale via /api/pos/reset directly, Hold
// Sale via #hold-modal's own form POSTing /api/pos/hold) without closing the
// overlay first, leaving a stale Tender panel showing payment state for a
// basket that no longer exists. Not a money/fiscal bug (a subsequent
// Complete Sale against the stale panel is rejected server-side), but a
// confusing UI state — this drives the real buttons and asserts the overlay
// actually closes, not just that the endpoints succeed.
test.describe('New Sale / Hold Sale close the payment overlay if it is open (ut-docs#1386)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('New Sale closes an open payment overlay', async ({ page }) => {
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

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      page.getByTestId('kiosk-checkout-start').click(),
    ]);
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
  });

  test('Hold Sale closes an open payment overlay', async ({ page }) => {
    // .payment-overlay is a fixed 26rem right-anchored panel (app.css); at
    // Playwright's default 1280x720 (and at 1024x600/1366x768) it covers
    // Hold Sale's real screen position almost entirely — measured live,
    // not assumed: at 1366x768 ~80% of Hold Sale's hit area sits under the
    // overlay, at 1024x600 ~100%. A real, unforced click only reliably
    // lands at a wide desktop viewport, where the overlay's fixed width is
    // a smaller fraction of the screen — measured clear (center point
    // outside the overlay) from 1600x900 up, comfortably so at 1920x1080,
    // a genuine desktop-shell resolution this product ships to (macOS/
    // Windows/Linux, .goreleaser.yaml). Explicit viewport, not the
    // project's ambient default, so this spec's pass/fail doesn't depend
    // on whichever viewport Playwright happens to default to.
    await page.setViewportSize({ width: 1920, height: 1080 });
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

    await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
    await expect(page.locator('#hold-modal')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      page.locator('#hold-modal button[type=submit]').click(),
    ]);
    await expect(page.locator('#hold-modal')).toBeHidden();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
  });
});
