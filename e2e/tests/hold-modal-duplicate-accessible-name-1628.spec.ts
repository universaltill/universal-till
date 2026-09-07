import { test, expect } from './fixtures';

// ut-docs#1628 (found reviewing #1625's own independent review):
// #payment-overlay's in-overlay Hold Sale copy already got its own
// distinguishing aria-label from #1625 so it no longer collides with the
// original .tender-default-footer trigger. But #hold-modal's own submit
// button is ALSO plain "Hold Sale", and #hold-modal opens non-modally too
// (same on-screen-keyboard reasoning as #payment-overlay — nothing outside
// it becomes inert). So once an operator taps either Hold Sale trigger
// (original or in-overlay copy) to open the naming dialog, its submit
// button joins the a11y tree as a THIRD "Hold Sale"-named control — same
// bug class #1625 fixed for the other two, pre-existing (predates
// ut-docs#1542) and reachable in exactly this flow.
test.describe('hold-modal submit button has its own distinguishing accessible name (ut-docs#1628)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('opening hold-modal from the in-overlay Hold Sale copy leaves exactly one plainly-named "Hold Sale" control, and the modal submit button is distinctly named', async ({ page }) => {
    // 1024x600 (this product's own kiosk floor) — same viewport #1625 used,
    // so both the original trigger and the in-overlay copy are
    // simultaneously present and interactive.
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

    // Open the dialog via the in-overlay copy — the exact repro path the
    // issue names (the original trigger reaches the same dialog too, but
    // this is the one #1625's own new spec already drives).
    const overlayHoldCopy = page.getByTestId('payment-overlay-hold');
    await overlayHoldCopy.click();
    await expect(page.locator('#hold-modal')).toBeVisible();

    // With the dialog open, all three controls are simultaneously in the
    // tree: the original trigger (plain "Hold Sale"), the in-overlay copy
    // (already disambiguated by #1625 as "Hold Sale (Tender panel)"), and
    // the dialog's own submit button (this card's fix). Exactly ONE control
    // is named plainly "Hold Sale" — the original trigger — never two or
    // three. `exact: true` matters: without it, Playwright's substring
    // matching would count every one of these (each name still contains
    // "Hold Sale" as a substring), defeating the point of this assertion.
    await expect(page.getByRole('button', { name: 'Hold Sale', exact: true })).toHaveCount(1);
    await expect(overlayHoldCopy).toHaveAccessibleName('Hold Sale (Tender panel)');

    const modalSubmit = page.locator('#hold-modal button[type=submit]');
    await expect(modalSubmit).toBeVisible();
    await expect(modalSubmit).toHaveAccessibleName('Hold Sale (confirm)');

    // The fix must not have broken the actual hold-and-close behavior —
    // same regression shape as #1625's own equivalent check.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      modalSubmit.click(),
    ]);
    await expect(page.locator('#hold-modal')).toBeHidden();
  });
});
