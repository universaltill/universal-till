import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#925: the split-tender panel's OWN status/validation copy lives in
// web/public/app.js, a shipped static file guard-i18n.sh does not scan
// (universal-till/CLAUDE.md, "Known gap"). It used to hardcode English
// literals -- a Persian-locale sale showed "Sale completed." next to
// otherwise fully-Persian UI. The strings now ride on data-msg-* attributes
// on #split-tender-card (the same bridge #barcode-scan-overlay uses).
//
// The Go-level tests pin that the attributes are rendered and carry the right
// locale's value; this pins the half they cannot see -- that app.js actually
// READS them at runtime, so what the operator ends up looking at is Persian.
//
// Assertions here are deliberately POSITIVE (the expected fa string), not
// `not.toContainText('English')`. An independent review of the first draft
// showed the negative form passes trivially when a locator matches nothing or
// an element is empty, and one such assertion was outright unreachable.
const FA = {
  noPending: 'پرداخت در انتظاری نیست.',
  needPayment: 'پیش از تکمیل فروش حداقل یک پرداخت اضافه کنید.',
  amountPositive: 'مبلغ باید بزرگ‌تر از صفر باشد.',
  saleCompleted: 'فروش تکمیل شد.',
  // 'پرداخت %s به مبلغ %s افزوده شد.' / '(باقی‌مانده %s)' -- matched by their
  // locale-stable fragments, since the %s values are runtime data.
  addedPrefix: 'پرداخت',
  addedMiddle: 'به مبلغ',
  addedSuffix: 'افزوده شد.',
  changeNote: 'باقی‌مانده',
};

test.describe('split-tender panel status copy localizes (ut-docs#925)', () => {
  test.beforeEach(async ({ page }) => {
    // The basket lives in the server-global engine, shared by every spec on
    // this till -- without this, a line left behind by an earlier spec (e.g.
    // settings-osk.spec.ts's cancelled hold-sale dialog test, which leaves a
    // Coca-Cola in the basket on purpose) breaks this file's exact-total
    // arithmetic (ut-docs#1310). Mirrors sale-screen-osk-scan-submit-1177.
    // spec.ts's own beforeEach reset for the same reason.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('fa/RTL: validation, add-with-change and a COMPLETED sale all render Persian', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await page.waitForSelector('.pos-container');
    // ut-docs#1252: the Pay/Split tabs now live inside the #payment-overlay
    // dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab').nth(1).click();

    const list = page.locator('#split-tender-payments');
    const status = page.locator('#split-tender-status');

    // Empty state -- rendered by renderPayments() from data-msg-no-pending,
    // not by the server's initial markup, so this is app.js's own output.
    await expect(list).toContainText(FA.noPending);

    // 1. Validation path: submit with nothing pending.
    await page.locator('#split-tender-submit').click();
    await expect(status).toHaveText(FA.needPayment);

    // 2. Local validation: a zero amount never reaches the server.
    await page.locator('#split-tender-form select[name="method"]').selectOption({ index: 0 });
    await page.locator('#split-tender-form input[name="amount"]').fill('0');
    await page.locator('#split-tender-add').click();
    await expect(status).toHaveText(FA.amountPositive);

    // 3. Add a real payment WITH change, so the change-note fragment inside
    //    the payment pill is genuinely exercised (the first draft added a
    //    payment with change 0, making its change-note assertion unreachable).
    // ut-docs#1252: close the overlay before scanning, matching the real
    // operator flow (same as tender-panel-reachable.spec.ts). It used to be
    // a MODAL dialog that blocked pointer events on the rest of the page
    // (scan-row included) while open, making this a hard requirement, not
    // just a flow preference -- ut-docs#1385 made it non-modal (the
    // on-screen keyboard needed to stay tappable while it's open), so that
    // block no longer applies, but closing first still matches how an
    // operator actually works and is kept unchanged.
    await page.getByTestId('payment-close').click();
    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');
    // ut-docs#1252: the Pay/Split tabs now live inside the #payment-overlay
    // dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab').nth(1).click();
    await page.locator('#split-tender-form select[name="method"]').selectOption({ index: 0 });
    await page.locator('#split-tender-form input[name="amount"]').fill('1.40');
    await page.locator('#split-tender-form input[name="change"]').fill('0.20');
    await page.locator('#split-tender-add').click();
    await page.waitForSelector('.payment-pill');

    await expect(status).toContainText(FA.addedPrefix);
    await expect(status).toContainText(FA.addedMiddle);
    await expect(status).toContainText(FA.addedSuffix);
    await expect(list).toContainText(FA.changeNote);
    // Both %s substitutions really happened -- a dropped placeholder would
    // leave the amount off this confirmation entirely.
    await expect(status).toContainText('1.20');

    // 4. Drive it all the way through a SUCCESSFUL sale. The net payment
    //    (1.40 - 0.20 change = 1.20) covers the 1.20 total, so this reaches
    //    the real success branch -- the one that renders "Sale completed.",
    //    the exact string this card was raised for. The first draft never
    //    completed a sale, leaving that string unexercised at runtime.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#split-tender-submit').click(),
    ]);
    await expect(status).toHaveText(FA.saleCompleted);
    await expect(page.locator('.payment-pill')).toHaveCount(0);
    await expect(list).toContainText(FA.noPending);

    assertClean();
  });

  test('en is unchanged: the same statuses still render the English copy', async ({ page }) => {
    await page.goto('/?lang=en');
    await page.waitForSelector('.pos-container');
    // ut-docs#1252: the Pay/Split tabs now live inside the #payment-overlay
    // dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab').nth(1).click();

    await expect(page.locator('#split-tender-payments')).toContainText('No pending payments yet.');

    await page.locator('#split-tender-submit').click();
    await expect(page.locator('#split-tender-status')).toHaveText(
      'Add at least one payment before completing the sale.',
    );
  });
});
