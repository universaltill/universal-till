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

  test('at 1920x1080 (not covered, per #1386) the originals stay in the tab order while the overlay is open', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const originalNewSale = page.getByTestId('kiosk-checkout-start');
    const originalHold = page.getByTestId('tender-footer-hold');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    // Self-justifying, same reasoning as the two #1542 hit-test assertions:
    // pins that these originals are NOT geometrically covered at this
    // viewport, which is exactly why they must stay reachable.
    const isCovered = (locator: ReturnType<typeof page.getByTestId>) =>
      locator.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const overlay = document.getElementById('payment-overlay')!;
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!at && (at === overlay || overlay.contains(at));
      });
    expect(await isCovered(originalNewSale), 'ORIGINAL New Sale must not be covered at 1920x1080').toBe(false);
    expect(await isCovered(originalHold), 'ORIGINAL Hold Sale must not be covered at 1920x1080').toBe(false);

    await expect(originalNewSale).not.toHaveAttribute('tabindex', '-1');
    await expect(originalHold).not.toHaveAttribute('tabindex', '-1');
  });
});
