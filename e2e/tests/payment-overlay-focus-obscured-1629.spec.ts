import { test, expect } from './fixtures';

// ut-docs#1629 (found reviewing #1625): #1625 gave the ORIGINAL Hold Sale /
// New Sale buttons in .tender-default-footer their own unambiguous
// accessible name, but at desktop viewports where the open, non-modal
// #payment-overlay geometrically covers them (measured live up to
// ~1440px, see payment-overlay-footer-reachable-1542.spec.ts), they stay
// in the keyboard tab order with no visible focus indicator anywhere on
// screen — WCAG 2.2 SC 2.4.11 (Focus Not Obscured). A blanket `inert` on
// .tender-default-footer was already rejected by #1625's own review: it
// would also disable these buttons at WIDE viewports where they are NOT
// covered and are legitimately keyboard-reachable
// (new-sale-closes-payment-overlay-1386.spec.ts drives the ORIGINAL Hold
// Sale button directly at 1920x1080 with the overlay open). The fix is
// narrower: `tabindex="-1"` on just these two originals, applied only
// while the overlay is open AND only while they are actually covered —
// this spec pins both the covered-narrow and the reachable-wide cases so
// neither regresses into the other.
test.describe('covered originals drop out of tab order while the payment overlay covers them (ut-docs#1629)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('at 1024x600 (covered, per #1542) the originals get tabindex=-1 while open and lose it again on close', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    const originalNewSale = page.getByTestId('kiosk-checkout-start');
    const originalHold = page.getByTestId('tender-footer-hold');

    // Baseline: normally focusable before the overlay ever opens.
    await expect(originalNewSale).not.toHaveAttribute('tabindex', '-1');
    await expect(originalHold).not.toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await expect(originalNewSale).toHaveAttribute('tabindex', '-1');
    await expect(originalHold).toHaveAttribute('tabindex', '-1');

    // The in-overlay duplicates (#1542) stay fully reachable — this card
    // must not touch them.
    await expect(page.getByTestId('payment-overlay-new-sale')).not.toHaveAttribute('tabindex', '-1');
    await expect(page.getByTestId('payment-overlay-hold')).not.toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    await expect(originalNewSale).not.toHaveAttribute('tabindex', '-1');
    await expect(originalHold).not.toHaveAttribute('tabindex', '-1');
  });

  // ut-docs#2702: the action row is now Pay first, then the Hold / New sale
  // / Open orders icons at the row's inline END -- which, at 1920x1080, is
  // under the overlay (it opens over the end of the right-hand column), so
  // the originals are no longer the "not covered" negative control there;
  // Pay is. The invariant this spec exists for is unchanged and asserted
  // per control: tabindex=-1 exactly when the overlay covers it, never
  // otherwise. (While covered, the overlay's own footer copies of Hold /
  // New sale stay reachable -- payment-overlay-footer-reachable-1542.)
  test('at 1920x1080 each action-row control is out of the tab order exactly when the overlay covers it', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    const isCovered = (locator: ReturnType<typeof page.getByTestId>) =>
      locator.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const overlay = document.getElementById('payment-overlay')!;
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!at && (at === overlay || overlay.contains(at));
      });
    // Negative control: the row's leading Pay button is NOT covered here.
    expect(await isCovered(page.getByTestId('payment-open')), 'Pay must not be covered at 1920x1080').toBe(false);
    await expect(page.getByTestId('payment-open')).not.toHaveAttribute('tabindex', '-1');

    for (const id of ['tender-footer-hold', 'kiosk-checkout-start', 'parked-orders-open']) {
      const el = page.getByTestId(id);
      if (await isCovered(el)) {
        await expect(el, `${id} is covered, so it must be out of the tab order`).toHaveAttribute('tabindex', '-1');
      } else {
        await expect(el, `${id} is not covered, so it must stay in the tab order`).not.toHaveAttribute('tabindex', '-1');
      }
    }
  });
});
