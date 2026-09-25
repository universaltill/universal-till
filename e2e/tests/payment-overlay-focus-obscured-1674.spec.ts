import { test, expect } from './fixtures';

// ut-docs#1674 (found reviewing #1629, the reviewer's own coverage sweep):
// #1629 gave the two ORIGINAL Hold Sale / New Sale buttons `tabindex="-1"`
// while the open, non-modal #payment-overlay geometrically covers them —
// but deliberately excluded three more controls that the same review swept
// and found in the identical state (WCAG 2.2 SC 2.4.11, Focus Not
// Obscured): the Payment trigger itself, `.tender-quickpay`'s one-tap
// charge button, and the phone-width New Sale duplicate. This spec extends
// the same, already-shipped mechanism (`updateFocusability()`'s `targets`
// array in web/public/app.js) to all three — no new coverage/restore logic,
// just three more elements sharing the identical `isCoveredByOverlay()`
// hit-test.
test.describe('remaining covered controls drop out of tab order while the payment overlay covers them (ut-docs#1674)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  // ut-docs#2702: the quick-pay test that sat here went with the quick-pay
  // button (its one-tap job moved inside the payment overlay itself, so it
  // can no longer be covered BY that overlay). The remaining action-row
  // controls are covered by payment-overlay-focus-sweep-1702.spec.ts.

  test('the Payment trigger itself gets tabindex=-1 while the overlay it opens covers it (1024x600), stays in the tab order where it is not covered (1920x1080, ut-docs#2702) — and stays clickable to open the overlay in the first place', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    const paymentOpen = page.getByTestId('payment-open');
    await expect(paymentOpen).not.toHaveAttribute('tabindex', '-1');

    // tabindex=-1 removes a control from the keyboard TAB order; it must
    // never prevent the control's own .click() from still opening the
    // overlay — this is the one target of the three whose entire purpose
    // is opening it, so a regression here is the highest-consequence one.
    await paymentOpen.click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(paymentOpen).toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(paymentOpen).not.toHaveAttribute('tabindex', '-1');

    // ut-docs#2702: Pay now LEADS the action row (inline start), so at a
    // wide desktop viewport it sits clear of the overlay, which opens over
    // the end of the right-hand column -- the negative control this test
    // could not have before: not covered, so it stays in the tab order.
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const paymentOpenWide = page.getByTestId('payment-open');
    await paymentOpenWide.click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    const coveredWide = await paymentOpenWide.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const overlay = document.getElementById('payment-overlay')!;
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === overlay || overlay.contains(at));
    });
    expect(coveredWide, 'Pay leads the row and must not be covered at 1920x1080').toBe(false);
    await expect(paymentOpenWide).not.toHaveAttribute('tabindex', '-1');
  });

  test('the phone-width New Sale duplicate gets tabindex=-1 while covered at 375x667 (overlay goes full-screen), and the existing not-rendered guard still no-ops it at 1024x600 where it is display:none', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    const phoneNewSale = page.getByTestId('kiosk-checkout-start-phone');
    await expect(phoneNewSale).toBeVisible();
    await expect(phoneNewSale).not.toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(phoneNewSale).toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(phoneNewSale).not.toHaveAttribute('tabindex', '-1');

    // At >480px this element is display:none (.phone-fallback-only,
    // app.css) — the existing `r.width === 0 && r.height === 0` guard in
    // isCoveredByOverlay() already treats "not rendered" as "not covered",
    // so it must never gain tabindex=-1 here regardless of the overlay.
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const phoneNewSaleDesktop = page.getByTestId('kiosk-checkout-start-phone');
    await expect(phoneNewSaleDesktop).toBeHidden();
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(phoneNewSaleDesktop).not.toHaveAttribute('tabindex', '-1');
  });
});
