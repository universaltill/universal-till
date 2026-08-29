import { test, expect } from '@playwright/test';
import { setOskMode } from './helpers';

// ut-docs#1276: osk.js's numeric ('num') layer was digits + '.' + '⌫' + '↵'
// only — no '-' key, and no way to expose one — so a field that legitimately
// accepts a negative amount could never have one typed on a real touch till.
// Found by independent review of ut-docs#1272's fix; confirmed pre-existing
// there (shifts-tips-osk-1272.spec.ts's own adjustment-form test could only
// exercise the positive/"adjustment" direction, never a payout) and again by
// osk-decimal-admin-fields-1275.spec.ts's inventory quantity test, which had
// to fall back to page.fill() to prove the pattern still accepted '-' at all.
//
// Fix: a second numeric layout, `numSigned`, with a '-' key, shown instead of
// the plain `num` layer only for a field whose own HTML `pattern` already
// declares it accepts a leading '-' (isSigned() in osk.js) — the exact
// convention shifts.html's payout/adjustment amount and inventory.html's
// stock quantity/override fields already use
// (pattern="-?[0-9]+(\.[0-9]{1,2})?"). A plain positive-only numeric field
// (no such pattern) must keep getting the unchanged `num` layer, with no
// minus key to misuse.

async function typeViaOsk(page: import('@playwright/test').Page, text: string) {
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

// Mirrors deposit-refund-payout-osk-1249.spec.ts's own helper: the open-shift
// form ships a pre-selected register and pre-filled opening cash, so a plain
// submit suffices when no shift is already open.
async function ensureShiftOpen(page: import('@playwright/test').Page) {
  await page.goto('/shifts');
  const openForm = page.locator('#open-shift-form');
  if (await openForm.count()) {
    await openForm.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  }
}

// Closes whatever shift is open, driven directly via fetch (not the UI/OSK —
// this is teardown, not part of what's under test). Same pattern as
// shifts-tips-osk-1272.spec.ts's own ensureNoShiftOpen: this spec's payout
// test opens a shift and records an adjustment against it, and every spec
// file on this shared till server (playwright.config.ts reuses one server
// across a project's whole run) must leave it in a clean, no-shift-open
// state for whichever spec alphabetically runs next (independent review,
// ut-docs#1276) — otherwise a later spec asserting absolute expected-cash
// inherits this test's own -25.00 adjustment.
async function ensureNoShiftOpen(page: import('@playwright/test').Page) {
  await page.goto('/shifts');
  const closeForm = page.locator('#close-shift-form');
  if (await closeForm.count()) {
    await page.evaluate(async () => {
      const shiftId = (document.querySelector('#close-shift-form input[name="shift_id"]') as HTMLInputElement).value;
      await fetch('/api/shifts/close', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ shift_id: shiftId, closing_cash: '0' }),
      });
    });
  }
}

test.describe('osk.js numeric minus key (ut-docs#1276)', () => {
  test.afterEach(async ({ page }) => {
    await setOskMode(page, 'auto');
  });

  test('a field with no signed pattern never shows a "-" key on the numeric layer', async ({ page }) => {
    await setOskMode(page, 'on');
    // #stock-cost is a plain positive-only numeric field (no leading '-?' in
    // its pattern) — the negative gate must keep it on the plain 'num' layer.
    await page.goto('/inventory');
    await page.locator('#stock-cost').click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await expect(page.locator('#osk button[data-k="-"]')).toHaveCount(0);
  });

  test('a field whose pattern allows a leading "-" shows the minus key', async ({ page }) => {
    await setOskMode(page, 'on');
    // inventory.html's `quantity` field declares pattern="-?[0-9]+…" —
    // "positive for receive, +/- for adjust" (internal/pages/inventory_api.go).
    await page.goto('/inventory');
    await page.locator('#stock-form input[name="quantity"]').click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await expect(page.locator('#osk button[data-k="-"]')).toBeVisible();
  });

  test('shift cash adjustment: a payout amount is typed as a real negative value via the on-screen keyboard and completes', async ({ page }) => {
    await setOskMode(page, 'on');
    await ensureShiftOpen(page);

    await page.locator('details.catalog-extra summary', { hasText: /adjustment/i }).click();
    await page.locator('#adjustment-form select[name="type"]').selectOption('payout');

    const amount = page.locator('#adjust-pounds');
    await amount.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '-25.00');
    await expect(amount).toHaveValue('-25.00');
    await expect(page.locator('#adjust-minor')).toHaveValue('-2500');

    await page.locator('#adjustment-form input[name="reason"]').fill('e2e: ut-docs#1276 payout via OSK');

    const pin = page.locator('#adjustment-form input[name="manager_pin"]');
    await pin.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '1234');

    const [response] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/adjustment')),
      page.locator('#adjustment-form button[type=submit]').click(),
    ]);
    expect(response.status(), 'a correctly OSK-typed negative payout must not 400').toBe(200);
    await expect(page.locator('#shift-result')).not.toContainText('error');

    await ensureNoShiftOpen(page);
  });
});
