import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1851: a voucher whose balance is LESS than the sale total must be
// combinable with a second payment method for the remainder, in the same
// sale. The backend (pos.CompleteSale / netPayments, sales.go) already
// supports a voucher leg alongside another leg in one `payments` array
// (ut-docs#1832 wired voucher_id into the Split tab's general
// addPayment()/payments-array flow), and internal/pos's own
// TestCompleteSale_VoucherPlusCashSplitTender pins that at the Go level --
// but nothing before this spec drove the actual Split-tab UI through that
// combination, the same seam split-tender-underpayment-921.spec.ts and the
// voucher-scan-pay-amount-1833.spec.ts review both exist to cover for their
// own features (a UI-wiring bug can exist even when the handler underneath
// is correct).
test.describe('Split tab combines a tracked voucher with a second payment method (ut-docs#1851)', () => {
  test.afterEach(async ({ page }) => {
    // Server-side reset regardless of UI state (e2e/README rule) -- also
    // clears any still-pending voucher (pos.Service.resetLocked).
    await page.request.post('/api/pos/reset');
  });

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

  test('voucher (partial) + cash (remainder) via the Split tab completes the sale and debits the voucher', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    const code = `GS-E2E-SPLIT-${Date.now()}`;
    // itm001 (EAN13 5000000000012) is 120 minor units due. A voucher worth
    // less than that (50) forces the real combination this card is about --
    // the remaining 70 must come from a second, differently-methoded leg.
    await issueVoucher(page, code, 50);

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();

    // Leg 1: the voucher, for its own balance (not the full amount due).
    await page.locator('#split-tender-form select[name="method"]').selectOption('voucher');
    await expect(page.locator('#split-tender-voucher-field')).toBeVisible();
    await page.locator('#split-tender-form input[name="voucher_id"]').fill(code);
    await page.locator('#split-tender-form input[name="amount"]').fill('0.50');
    await page.locator('#split-tender-add').click();
    await expect(page.locator('.payment-pill')).toHaveCount(1);

    // Leg 2: cash for the remainder -- #split-tender-fill computes exactly
    // what's still outstanding after leg 1's pending payment. Asserted
    // directly (not just the resulting pill count): a #split-tender-fill
    // regression that filled the full amount due (1.20) instead of the true
    // remainder (0.70) would still leave 2 pills and a covered/completed
    // sale -- the voucher's own balance still zeroing out either way -- so
    // only reading the field it actually wrote pins this leg's own amount.
    await page.locator('#split-tender-form select[name="method"]').selectOption('cash');
    await expect(page.locator('#split-tender-voucher-field')).toBeHidden();
    await page.locator('#split-tender-fill').click();
    await expect(page.locator('#split-tender-form input[name="amount"]')).toHaveValue('0.70');
    await page.locator('#split-tender-add').click();
    await expect(page.locator('.payment-pill')).toHaveCount(2);

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#split-tender-submit').click(),
    ]);

    // Must be the real success path -- no error toast, the status line says
    // so, and both surfaces the pipeline has previously found false-positive
    // bugs on (ut-docs#921's response.ok() check; ut-docs#1833's form-encoded
    // fallback) are checked directly rather than inferred.
    await expect(page.locator('#toast-message.error')).toHaveCount(0);
    await expect(page.locator('#split-tender-status')).toContainText('Sale completed.');
    await expect(page.locator('.payment-pill')).toHaveCount(0);
    await expect(page.locator('.basket table tbody tr')).toHaveCount(0);

    const res = await page.request.get(`/api/vouchers/${code}`);
    expect(res.ok(), `read back voucher ${code}: ${res.status()}`).toBeTruthy();
    const body = await res.json();
    // 50 balance, fully drawn down by its own leg (not the combined 120) --
    // a still-nonzero or still-50 balance means the voucher leg either never
    // debited or double-counted against the cash leg.
    expect(body.data.balance, 'voucher must be debited by exactly its own leg').toBe(0);
    expect(body.data.status).toBe('redeemed');

    assertClean();
  });
});
