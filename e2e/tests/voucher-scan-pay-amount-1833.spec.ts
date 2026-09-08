import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1833: scanning a Gutschein (voucher) barcode at the sale screen
// offers its balance as a pay-grid tender option (#pay-voucher-btn). The
// button's `amount` (rewritten into its own hx-vals by the page-local IIFE
// in web/ui/pages/index.html on every htmx:afterSwap) must be
// min(voucher balance, amount still due) in integer minor units — a
// partial redemption when the voucher covers MORE than the sale, and the
// full remaining balance when it covers LESS. This math lives entirely in
// client-side JS (no Go code computes it), so it is untestable at the Go
// level; nothing else in this repo (Go or e2e) exercised either case
// before this spec — written by Tester per the ut-docs#1833 verification
// pass, not by Dev.
//
// Vouchers are issued through the app's own real API (no cashier-facing
// issuing UI exists yet, per README) — the exact shape
// TestPOSTender_VoucherIssueAndRedeem (internal/pages/voucher_tender_test.go)
// already uses: an empty-basket sale whose only payment is cash for the
// voucher's own face value.
//
// Demo catalog prices (internal/data/seeddata/demo_catalogue.sql, already
// used by sale-screen-213.spec.ts's CODES list): EAN13 5000000000012 ->
// itm001 "Coca-Cola Can 330ml", base_price 120 (tax-inclusive display,
// per e2e/README's tax-inclusive-by-default correction) -- i.e. a 1.20
// line, 120 minor units due for a single scan.

async function scanCode(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function issueVoucher(page, code: string, balanceMinor: number) {
  const res = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: {
      payments: [{ method: 'cash', amount: balanceMinor }],
      issue_vouchers: [{ amount: balanceMinor, code }],
    },
  });
  expect(res.ok(), `issue voucher ${code}: ${res.status()} ${await res.text()}`).toBeTruthy();
}

async function payVoucherHxVals(page): Promise<{ amount: number; method: string; voucher_id: string }> {
  const raw = await page.locator('#pay-voucher-btn').getAttribute('hx-vals');
  expect(raw, 'pay-voucher-btn hx-vals present').toBeTruthy();
  return JSON.parse(raw!);
}

test.describe('scan-to-redeem voucher amount clamps to min(balance, due) (ut-docs#1833)', () => {
  test.afterEach(async ({ page }) => {
    // Server-side reset regardless of UI state (e2e/README rule) -- also
    // clears any still-pending voucher (pos.Service.resetLocked).
    await page.request.post('/api/pos/reset');
  });

  test('voucher balance GREATER than amount due: pay button clamps to the sale total (partial redemption)', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    const code = `GS-E2E-HI-${Date.now()}`;
    await issueVoucher(page, code, 5000); // 50.00 balance

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await scanCode(page, '5000000000012'); // itm001, 120 minor units due
    await scanCode(page, code);

    // #pay-voucher-btn lives in the Pay/Split tabs inside #payment-overlay
    // (a non-modal <dialog>, ut-docs#1385) -- like every other tender
    // button (Cash/Card), it only renders visible once that overlay is
    // open, same pattern tender-panel-reachable.spec.ts uses.
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await expect(page.locator('#pay-voucher-btn')).toBeVisible();
    const vals = await payVoucherHxVals(page);
    expect(vals.method).toBe('voucher');
    expect(vals.voucher_id).toBe(code);
    // Balance (5000) exceeds what's due (120) -- must clamp to the sale's
    // own total, leaving 4880 on the voucher for next time, NOT hand over
    // the whole balance as tender.
    expect(vals.amount, 'amount must clamp to amount due, not the full balance').toBe(120);

    assertClean();
  });

  test('voucher balance LESS than amount due: pay button offers the full remaining balance', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = `GS-E2E-LO-${Date.now()}`;
    await issueVoucher(page, code, 100); // 1.00 balance

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await scanCode(page, '5000000000012'); // itm001, 120 minor units due
    await scanCode(page, code);

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await expect(page.locator('#pay-voucher-btn')).toBeVisible();
    const vals = await payVoucherHxVals(page);
    expect(vals.method).toBe('voucher');
    expect(vals.voucher_id).toBe(code);
    // Balance (100) is less than due (120) -- the button must offer the
    // full remaining balance, not the (larger) amount still due, so the
    // rest can be taken with a second payment method.
    expect(vals.amount, 'amount must be the full voucher balance, not the (larger) amount due').toBe(100);

    assertClean();
  });

  // Independent review: the two tests above assert the button's hx-vals but
  // never CLICK it, which is how a real defect got this far -- #pay-voucher-btn
  // is a plain hx-vals button (no json-enc extension is registered anywhere
  // under web/ui), so it posts /api/pos/tender FORM-ENCODED and hits the
  // handler's form fallback, not the JSON `payments` branch that
  // TestPOSTender_VoucherIssueAndRedeem covers. That fallback dropped
  // voucher_id, so the tap completed the sale and printed a receipt while
  // leaving vouchers.balance untouched -- the voucher stayed fully spendable,
  // redeemable again without limit. Clicking for real and reading the balance
  // back through the app's own API is the only check that spans that seam.
  test('clicking the voucher pay button actually debits the voucher (tracked redemption)', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = `GS-E2E-PAY-${Date.now()}`;
    await issueVoucher(page, code, 5000); // 50.00 balance

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await scanCode(page, '5000000000012'); // itm001, 120 minor units due
    await scanCode(page, code);

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(page.locator('#pay-voucher-btn')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#pay-voucher-btn').click(),
    ]);

    const res = await page.request.get(`/api/vouchers/${code}`);
    expect(res.ok(), `read back voucher ${code}: ${res.status()}`).toBeTruthy();
    const body = await res.json();
    // 5000 - 120 = 4880: only what the sale needed was taken, and it really
    // was taken. A still-5000 balance means the redemption was recorded as
    // an untracked generic voucher payment.
    expect(body.data.balance, 'voucher must be debited by the clamped amount').toBe(4880);
    expect(body.data.status).toBe('active');

    assertClean();
  });
});
