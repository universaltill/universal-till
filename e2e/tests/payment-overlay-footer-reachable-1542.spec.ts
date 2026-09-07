import { test, expect } from './fixtures';

// ut-docs#1542: .tender-default-footer's Hold Sale and New Sale sit outside
// #payment-overlay, which is a fixed, right-anchored 26rem panel over the
// tender column. Measured live (getBoundingClientRect center-point
// coverage) at every desktop viewport up to ~1440px wide, Hold Sale's
// center point sits UNDER the open overlay — not reachable by a real,
// unforced click — and New Sale is covered too at the 1024x600 kiosk
// floor specifically. tender-panel-reachable.spec.ts's own sibling file
// (new-sale-closes-payment-overlay-1386.spec.ts) already documents this:
// its "Hold Sale closes an open payment overlay" test has to force the
// viewport to 1920x1080 to get a real click to land at all.
//
// Fix: duplicate Hold Sale / New Sale copies now render INSIDE
// #payment-overlay's own body (same identical hx-post/onclick wiring as
// the originals, new testids) — the same "duplicate for reachability"
// shape this codebase already used for the phone-width New Sale copy
// (kiosk-checkout-start-phone, ut-docs#1345). This spec drives the new
// in-overlay copies specifically, at the exact viewport (1024x600) the
// card's own measurement table showed as the worst case for both buttons.
test.describe('payment overlay footer duplicates are reachable while the overlay is open (ut-docs#1542)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const hitTest = (locator: ReturnType<typeof page.getByTestId>) =>
    locator.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });

  test('the in-overlay Hold Sale copy is a real hit-test target at 1024x600 and holds the sale', async ({ page }) => {
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

    // Self-justifying: pins the actual geometry problem this card fixes,
    // not just the presence of the new copies (independent review,
    // ut-docs#1542) — the ORIGINAL Hold Sale button (.tender-default-footer)
    // must still be genuinely covered by the open overlay at this viewport.
    // If a future change narrows/repositions the overlay so this ever turns
    // true, that's a real signal to revisit whether the duplicate is still
    // needed, not a assertion to silently delete.
    const originalHold = page.locator('.tender-default-footer button', { hasText: 'Hold Sale' });
    expect(await hitTest(originalHold), 'the ORIGINAL Hold Sale button must still be covered by the overlay at 1024x600 (that is why the in-overlay copy exists)').toBe(false);

    const holdCopy = page.getByTestId('payment-overlay-hold');
    await expect(holdCopy).toBeVisible();
    expect(await hitTest(holdCopy), 'in-overlay Hold Sale copy must be a real, unobstructed hit-test target').toBe(true);

    await holdCopy.click(); // no force: must be a genuinely landable click
    await expect(page.locator('#hold-modal')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      page.locator('#hold-modal button[type=submit]').click(),
    ]);
    await expect(page.locator('#hold-modal')).toBeHidden();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
  });

  test('the in-overlay New Sale copy is a real hit-test target at 1024x600 and resets the basket', async ({ page }) => {
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

    // Self-justifying, same reasoning as the Hold Sale test above.
    const originalNewSale = page.getByTestId('kiosk-checkout-start');
    expect(await hitTest(originalNewSale), 'the ORIGINAL New Sale button must still be covered by the overlay at 1024x600 (that is why the in-overlay copy exists)').toBe(false);

    const newSaleCopy = page.getByTestId('payment-overlay-new-sale');
    await expect(newSaleCopy).toBeVisible();
    expect(await hitTest(newSaleCopy), 'in-overlay New Sale copy must be a real, unobstructed hit-test target').toBe(true);

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      newSaleCopy.click(), // no force: must be a genuinely landable click
    ]);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
  });

  // RTL (fa): the new row is a plain .tender-footer grid (already
  // direction-agnostic, no left/right literals) — under dir="rtl" both
  // copies must still be real hit-test targets.
  test('both in-overlay copies stay real hit-test targets under RTL (fa) at 1024x600', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    const holdCopy = page.getByTestId('payment-overlay-hold');
    const newSaleCopy = page.getByTestId('payment-overlay-new-sale');
    // Explicit visibility checks first (independent review, ut-docs#1542):
    // without these, a regression that removed the copies entirely would
    // fail with a bare 30s "waiting for locator" timeout from inside the
    // hitTest helper rather than a clear "not found" here.
    await expect(holdCopy).toBeVisible();
    await expect(newSaleCopy).toBeVisible();
    expect(await hitTest(holdCopy), 'in-overlay Hold Sale copy must be reachable under RTL').toBe(true);
    expect(await hitTest(newSaleCopy), 'in-overlay New Sale copy must be reachable under RTL').toBe(true);
  });
});
