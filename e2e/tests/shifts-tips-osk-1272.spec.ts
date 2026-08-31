import { test, expect } from './fixtures';
import { setOskMode } from './helpers';

// ut-docs#1272: same defect as ut-docs#1249 (deposit-refund-payout-osk-1249
// .spec.ts), on five more fields across three dialogs — each was
// type="number" + onchange into a separate hidden minor-units field:
//
// 1. onchange never fires for osk.js's `input`-only keystrokes, so the
//    submitted amount was always empty on a real touch till.
// 2. type="number" silently resets .value to "" on a momentarily-invalid
//    decimal string while typing (e.g. "5." while typing "5.00"), which
//    would have corrupted any decimal entry even once (1) was fixed.
// 3. (found alongside, not present in #1249's own fix) each field
//    hardcoded `Math.round(parseFloat(v) * 100)` instead of reusing
//    window.utCurrency.toMinor() — wrong by 100x on any 0-decimal
//    currency (IRR/IRT/IQD/AFN/JPY); the e2e till here runs GBP (2
//    decimals) so this asserts the *conversion is delegated to
//    toMinor()*, not the 100x defect directly (that's covered by
//    toMinor()'s own unit coverage in app.js, not by this repo's e2e).
//
// Fields covered (one on-screen-keyboard-driven flow per dialog, matching
// deposit-refund-payout-osk-1249.spec.ts's pattern — types via osk.js's
// real keys, never page.fill()/type(), which bypass osk.js entirely and
// would pass even against the unfixed markup):
//   - web/ui/pages/shifts.html: #opening-cash, #closing-cash, #skim-pounds,
//     #adjust-pounds
//   - web/ui/partials/reports_tab_tips.html: #tips-amount

async function typeViaOsk(page: import('@playwright/test').Page, selector: string, text: string) {
  await page.locator(selector).click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

// Resets /shifts to a known "no shift open" state regardless of what an
// earlier spec file left behind on this shared till server (playwright.
// config.ts reuses one server across every spec in a project's run) —
// driven directly via fetch, not the UI/OSK, since this is setup, not
// part of what's under test.
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
    await page.goto('/shifts');
  }
}

test.describe('shifts + tips: money fields entered via the on-screen keyboard', () => {
  test.afterEach(async ({ page }) => {
    await setOskMode(page, 'auto'); // restore the shared server's default (helpers.ts)
  });

  test('shift open → cash adjustment → shift close: every OSK-typed amount is submitted correctly, none silently dropped or corrupted', async ({ page }) => {
    await setOskMode(page, 'on');
    await ensureNoShiftOpen(page);

    // --- #opening-cash (open-shift-form) ---------------------------------
    await page.goto('/shifts');
    await expect(page.locator('#open-shift-form')).toBeVisible();
    const openingField = page.locator('#opening-cash');
    await openingField.click();
    await openingField.selectText(); // select the prefilled carry-forward value so the first OSK key replaces it, not appends to it
    await typeViaOskFrom(page, '150.25');
    await expect(page.locator('#opening-cash-minor')).toHaveValue('15025');
    await expect(page.locator('#opening-cash')).toHaveValue('150.25');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/open')),
      page.locator('#open-shift-form button[type=submit]').click(),
    ]);
    await expect(page.locator('#close-shift-form')).toBeVisible();

    // --- #adjust-pounds (adjustment-form) --------------------------------
    // type="adjustment" + a positive amount — the negative/payout direction
    // (which needs osk.js's '-' key, ut-docs#1276) has its own dedicated
    // coverage in osk-signed-minus-key-1276.spec.ts.
    await page.locator('details.catalog-extra summary', { hasText: /adjustment/i }).click();
    await page.locator('#adjustment-form select[name="type"]').selectOption('adjustment');
    await typeViaOsk(page, '#adjust-pounds', '12.34');
    await expect(page.locator('#adjust-minor')).toHaveValue('1234');
    await page.locator('#adjustment-form input[name="reason"]').fill('float top-up');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/adjustment')),
      page.locator('#adjustment-form button[type=submit]').click(),
    ]);
    await expect(page.locator('#shift-result')).not.toContainText('error');

    // --- #closing-cash + #skim-pounds (close-shift-form) ------------------
    await typeViaOsk(page, '#closing-cash', '200.00');
    await expect(page.locator('#closing-cash-minor')).toHaveValue('20000');
    await typeViaOsk(page, '#skim-pounds', '50.00');
    await expect(page.locator('#skim-minor')).toHaveValue('5000');
    const [closeResponse] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/close')),
      page.locator('#close-shift-form button[type=submit]').click(),
    ]);
    expect(closeResponse.status(), 'a correctly OSK-typed close must not 400').toBe(200);
  });

  test('tips: an amount typed via the on-screen keyboard is submitted correctly, not silently empty', async ({ page }) => {
    await setOskMode(page, 'on');
    await page.goto('/reports');
    await page.locator('#report-tab-tips').click();
    await expect(page.locator('#tips-amount')).toBeVisible();

    await page.locator('select[name="cashier_id"]').selectOption({ index: 1 }); // first real worker, whoever seeded this till
    await typeViaOsk(page, '#tips-amount', '7.50');
    await expect(page.locator('#tips-amount-minor')).toHaveValue('750');

    const [response] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/reports/worker-allocations')),
      page.locator('form[hx-post="/api/reports/worker-allocations"] button[type=submit]').click(),
    ]);
    expect(response.status(), 'a correctly OSK-typed tip must not 400').toBe(200);
  });
});

// Same osk-key-press loop as typeViaOsk, split out only because the
// opening-cash field needs a select-all first (it ships prefilled with the
// carry-forward value, unlike every other field here which starts empty) —
// kept as a second helper rather than a parameter so the common case above
// stays a one-line call.
async function typeViaOskFrom(page: import('@playwright/test').Page, text: string) {
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}
