import type { Locator } from '@playwright/test';
import { test, expect } from './fixtures';

// ut-docs#1702 (found reviewing #1674, the reviewer's own coverage sweep):
// #1629/#1674 gave five SPECIFIC controls tabindex="-1" while the open,
// non-modal #payment-overlay geometrically covers them (WCAG 2.2 SC
// 2.4.11, Focus Not Obscured) — but that #1674 review, with its own fix
// already applied, measured 11 (1024x600) / 8 (1280x800) / 4 (1920x1080)
// OTHER focusable controls on the sale screen still covered by the
// overlay and still reachable, none of them in the hardcoded `targets`
// array. This spec drives the generalized fix: `updateFocusability()` now
// sweeps every focusable element outside `#payment-overlay` (querying
// fresh each run, not a load-time snapshot) via the exact same
// `isCoveredByOverlay()` hit-test, rather than one more hardcoded entry.
//
// Controls below are a representative subset of the review's 11/8/4,
// chosen to exercise every element *kind* the sweep must handle: a plain
// input (scan barcode), a link styled as a button (products-add-link), a
// roving-tabindex ARIA tab (the active category tab — deliberately NOT
// touched when it's the currently-inactive tabindex="-1" tabs, which is
// existing, correct app behavior this sweep must not disturb), a product
// tile button, and a plain submit button (scan-row's "Add"). Each
// assertion first pins the live geometry via the identical
// elementFromPoint hit-test the implementation itself uses (same pattern
// as payment-overlay-focus-obscured-1629.spec.ts's own 1920x1080 negative
// control) so this spec fails loudly on real markup/layout drift instead
// of silently asserting against a stale assumption.
test.describe('the payment overlay focus sweep covers every focusable control it geometrically obscures, not just the hand-picked five (ut-docs#1702)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  function isCovered(locator: Locator) {
    return locator.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const overlay = document.getElementById('payment-overlay')!;
      if (r.width === 0 && r.height === 0) return false;
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === overlay || overlay.contains(at));
    });
  }

  test('at 1024x600, the scan input / products-add-link / active category tab / a product tile / the scan-row Add button all drop out of the tab order while covered, and every one regains it on close', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const scanInput = page.locator('.scan-row input[name="code"]');
    const addLink = page.getByTestId('products-add-link');
    // Selected by the "active" class (which Alpine toggles independently
    // of tabindex), not by its baseline tabindex="0" — the sweep itself
    // changes that attribute mid-test, and a selector keyed on it would
    // stop matching the moment the fix does its job.
    const activeTab = page.locator('.products-finder [role="tab"].active');
    const firstTile = page.locator('.btn-tile').first();
    const scanAdd = page.locator('.scan-row button[type="submit"]');
    const controls = [scanInput, addLink, activeTab, firstTile, scanAdd];

    // ut-docs#1984: scan an item first — Payment is disabled on an empty
    // basket. Doesn't affect the baseline captured below (scanning changes
    // basket contents, not these controls' tabindex).
    await scanInput.fill('5000000000012');
    await scanAdd.click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // Baseline: none pre-emptively disabled before the overlay ever opens.
    // Captured (not just asserted "not -1") so the close-path check below
    // can confirm an EXACT round-trip, not just "isn't -1 anymore" — the
    // active category tab's real baseline is tabindex="0", not absent, and
    // a restore bug that always wrote back "0" regardless of what was there
    // before would pass a bare `.not.toHaveAttribute('tabindex','-1')` on
    // every one of these controls yet still be wrong for any element whose
    // real baseline isn't "0" (independently confirmed possible: a review
    // mutation that hardcoded the restore value survived this spec before
    // this baseline-capture/round-trip check existed).
    const baseline: (string | null)[] = [];
    for (const el of controls) {
      await expect(el).not.toHaveAttribute('tabindex', '-1');
      baseline.push(await el.getAttribute('tabindex'));
    }

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    // Confirm the real, current geometry actually covers each one at this
    // width before asserting the fix's effect — a stale assumption here
    // would make this spec pass for the wrong reason.
    for (const el of controls) {
      expect(await isCovered(el), 'expected this control to be geometrically covered at 1024x600').toBe(true);
      await expect(el).toHaveAttribute('tabindex', '-1');
      // The save-marker must exist while we're mid-cover — the restore
      // path below depends on it, and a stale/missing marker here would
      // make the close-path assertions pass for the wrong reason.
      await expect(el).toHaveAttribute('data-a11y-tabindex-saved');
    }

    // The overlay's OWN controls must never be touched by the sweep.
    await expect(page.getByTestId('payment-close')).not.toHaveAttribute('tabindex', '-1');
    await expect(page.getByTestId('payment-overlay-new-sale')).not.toHaveAttribute('tabindex', '-1');
    await expect(page.getByTestId('payment-overlay-hold')).not.toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    // Exact round-trip, not just "not -1" — and the save-marker itself must
    // be cleaned up, not left behind to corrupt a LATER open/close cycle
    // (independently confirmed: leaving it behind lets a second cover/
    // restore cycle after a tab switch resurrect a stale saved value and
    // leave two category tabs simultaneously keyboard-reachable, breaking
    // the roving-tabindex pattern tab-bar-overflow-aria-424.spec.ts exists
    // to protect — that spec never catches it because it never crosses an
    // overlay open/close cycle itself).
    for (let i = 0; i < controls.length; i++) {
      const el = controls[i];
      const want = baseline[i];
      if (want === null) {
        await expect(el).not.toHaveAttribute('tabindex');
      } else {
        await expect(el).toHaveAttribute('tabindex', want);
      }
      await expect(el).not.toHaveAttribute('data-a11y-tabindex-saved');
    }
  });

  test('an INACTIVE category tab (tabindex=-1 by the existing roving-tabindex pattern, not our sweep) is left alone while the overlay is open', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // .first(), not an exact count — this asserts "at least one inactive
    // tab exists and stays untouched", not "the demo catalogue has exactly
    // N categories" (a seed-data detail unrelated to what this test
    // actually checks, and liable to change independently of this file).
    const inactiveTab = page.locator('.products-finder [role="tab"][tabindex="-1"]').first();
    await expect(inactiveTab).toHaveAttribute('tabindex', '-1');

    // ut-docs#1984: scan an item first — Payment is disabled on an empty
    // basket.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type="submit"]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    // Still -1 — but NOT because our sweep touched it (it never carries
    // the save marker, so closing the overlay must not suddenly make it
    // focusable, which would be a real regression of the existing
    // WAI-ARIA tabs pattern).
    await expect(inactiveTab).toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(inactiveTab).toHaveAttribute('tabindex', '-1');
  });

  test('at 1920x1080, products-add-link and the scan-row Add button are still covered and drop out of the tab order (per the review\'s 4-control 1920x1080 measurement)', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const addLink = page.getByTestId('products-add-link');
    const scanAdd = page.locator('.scan-row button[type="submit"]');

    // ut-docs#1984: scan an item first — Payment is disabled on an empty
    // basket.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await scanAdd.click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    for (const el of [addLink, scanAdd]) {
      expect(await isCovered(el), 'expected this control to be geometrically covered at 1920x1080').toBe(true);
      await expect(el).toHaveAttribute('tabindex', '-1');
    }

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    for (const el of [addLink, scanAdd]) {
      await expect(el).not.toHaveAttribute('tabindex', '-1');
    }
  });

  // Covers 4 of the 5 old explicit targets at a desktop-class viewport
  // (kiosk-checkout-start, tender-footer-hold, payment-open, quick-pay).
  // The 5th, kiosk-checkout-start-phone, only renders at phone width
  // (.kiosk-header.phone-fallback-only is CSS-hidden otherwise) and stays
  // covered by payment-overlay-focus-obscured-1674.spec.ts's own dedicated
  // 375x667 test — no coverage is lost, this test just isn't the one
  // driving it.
  test('the pre-existing explicit targets (New Sale, Hold Sale, Payment, quick-pay) keep working exactly as before, driven through the same generalized sweep', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const originalNewSale = page.getByTestId('kiosk-checkout-start');
    const originalHold = page.getByTestId('tender-footer-hold');
    const paymentOpen = page.getByTestId('payment-open');
    const quickPay = page.getByTestId('quick-pay');

    // ut-docs#1984: scan an item first — Payment is disabled on an empty
    // basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type="submit"]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await paymentOpen.click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await expect(originalNewSale).toHaveAttribute('tabindex', '-1');
    await expect(originalHold).toHaveAttribute('tabindex', '-1');
    await expect(paymentOpen).toHaveAttribute('tabindex', '-1');
    await expect(quickPay).toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    await expect(originalNewSale).not.toHaveAttribute('tabindex', '-1');
    await expect(originalHold).not.toHaveAttribute('tabindex', '-1');
    await expect(paymentOpen).not.toHaveAttribute('tabindex', '-1');
    await expect(quickPay).not.toHaveAttribute('tabindex', '-1');
  });

  // ut-docs#1702's whole reason for a fresh-queried sweep (candidates() run
  // on every open, never a load-time snapshot) rather than one more static
  // array is that the sale screen's controls are DYNAMIC — the review's own
  // "4 product tiles" and held-sales examples. This test proves that
  // property specifically: a held-sale chip that DID NOT EXIST when the
  // page first loaded must still be covered/restored correctly the very
  // first time the overlay opens after it appears. A cached, load-time
  // candidate list (the wrong implementation this design deliberately
  // rejected) would pass every other test in this file — none of them
  // change the DOM between page load and the first overlay-open — and only
  // this one would catch it.
  test('a held-sale chip that appears AFTER page load (not present when app.js first ran) is still covered by the sweep the next time the overlay opens', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // Scan an item and hold the sale — this both creates the basket line
    // and, on success, appends a NEW .held-chip button to #held-sales that
    // did not exist anywhere in the DOM when this page's app.js IIFE ran.
    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type="submit"]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('tender-footer-hold').click();
    await expect(page.locator('#hold-modal')).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      page.locator('#hold-modal button[type="submit"]').click(),
    ]);
    await expect(page.locator('#hold-modal')).toBeHidden();

    const heldChip = page.locator('#held-sales .held-chip').first();
    await expect(heldChip).toBeVisible();
    await expect(heldChip).not.toHaveAttribute('tabindex', '-1');

    // ut-docs#1984: holding parked the basket, leaving it empty — Payment
    // is disabled on an empty basket, so scan a fresh item before opening
    // it again (this test's own point is the held CHIP's coverage, not
    // this basket's contents).
    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type="submit"]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    expect(await isCovered(heldChip), 'expected the held-sale chip to be geometrically covered at 1024x600 (same tender column as the scan-row controls)').toBe(true);
    await expect(heldChip).toHaveAttribute('tabindex', '-1');

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(heldChip).not.toHaveAttribute('tabindex', '-1');
  });
});
