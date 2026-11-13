import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#6153: Android System WebView ignores window.print(), so the
// Android app's bridge (window.AndroidKiosk.print) opens the system dialog.
// The web's one print entry point (UT.print) uses the bridge when the shell
// has it and window.print() otherwise; a 451 never prints on either path.
// window.print and the bridge are stubbed (no real dialog headless).
// The e2e till has no receipt-policy "ask" plugin, so that prompt is
// injected into the receipt screen with the same markup receipt.html renders.

type Variant = 'browser' | 'bridge' | 'bridge-false';

async function stub(page: Page, variant: Variant) {
  await page.addInitScript((v) => {
    const w = window as any;
    w.__prints = 0; w.__bridge = 0;
    w.print = () => { w.__prints++; };
    if (v !== 'browser') {
      w.AndroidKiosk = { print() { w.__bridge++; return v === 'bridge'; } };
    }
  }, variant);
}

async function counts(page: Page) {
  return page.evaluate(() => ({ prints: (window as any).__prints, bridge: (window as any).__bridge }));
}

async function sellToReceipt(page: Page) {
  await page.request.post('/api/pos/reset');
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.locator('#payment-overlay').getByTestId('pay-default').click(),
  ]);
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
}

test.afterEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });

const CASES: { variant: Variant; prints: number; bridge: number }[] = [
  { variant: 'browser', prints: 1, bridge: 0 },
  { variant: 'bridge', prints: 0, bridge: 1 },
  { variant: 'bridge-false', prints: 0, bridge: 1 },
];

for (const c of CASES) {
  test(`receipt Print, reprint fails (500): ${c.variant}`, async ({ page }) => {
    await stub(page, c.variant);
    await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 500, body: 'x' }));
    await sellToReceipt(page);
    const btn = page.locator('[data-action^="receipt-print:"]');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
      btn.click(),
    ]);
    await expect.poll(() => counts(page)).toEqual({ prints: c.prints, bridge: c.bridge });
  });

  test(`receipt Print, reprint 451 prints nothing: ${c.variant}`, async ({ page }) => {
    await stub(page, c.variant);
    await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 451, body: 'x' }));
    await sellToReceipt(page);
    const btn = page.locator('[data-action^="receipt-print:"]');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
      btn.click(),
    ]);
    await expect(btn).toBeDisabled();
    expect(await counts(page)).toEqual({ prints: 0, bridge: 0 });
  });

  test(`receipt ask prompt Print, reprint fails (500): ${c.variant}`, async ({ page }) => {
    await stub(page, c.variant);
    await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 500, body: 'x' }));
    await sellToReceipt(page);
    // Same markup as receipt.html's {{ if eq .ReceiptPolicy "ask" }} block.
    await page.evaluate(() => {
      const no = document.querySelector('[data-action^="receipt-print:"]')!.getAttribute('data-action')!.split(':')[1];
      const d = document.createElement('div');
      d.id = 'receipt-ask';
      d.innerHTML = `<button type="button" data-testid="ask-print" data-action="receipt-ask-print:${no}">Print</button>`;
      document.querySelector('#basket')!.appendChild(d);
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
      page.getByTestId('ask-print').click(),
    ]);
    await expect(page.locator('#receipt-ask')).toBeHidden();
    await expect.poll(() => counts(page)).toEqual({ prints: c.prints, bridge: c.bridge });
  });

  test(`receipt ask prompt, 451 prints nothing: ${c.variant}`, async ({ page }) => {
    await stub(page, c.variant);
    await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 451, body: 'x' }));
    await sellToReceipt(page);
    await page.evaluate(() => {
      const no = document.querySelector('[data-action^="receipt-print:"]')!.getAttribute('data-action')!.split(':')[1];
      const d = document.createElement('div');
      d.id = 'receipt-ask';
      d.innerHTML = `<button type="button" data-testid="ask-print" data-action="receipt-ask-print:${no}">Print</button>`;
      document.querySelector('#basket')!.appendChild(d);
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
      page.getByTestId('ask-print').click(),
    ]);
    await expect(page.locator('#receipt-ask')).toBeHidden();
    expect(await counts(page)).toEqual({ prints: 0, bridge: 0 });
  });
}

// The voucher "Print slip" browser fallback (#6152) ends in the same helper.
const RUN = Date.now().toString(36).toUpperCase();
const CODE = `T6153-${RUN}`;

test.describe.serial('voucher slip browser fallback (ut-docs#6153)', () => {
  test('import a voucher', async ({ page }) => {
    await page.goto('/vouchers?lang=en');
    await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
      name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${CODE},25.00,Spec holder\n`),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
      page.locator('#vouchers-import-form button[type=submit]').click(),
    ]);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
      page.locator('#vouchers-import-result button[type=submit]').click(),
    ]);
    await expect(page.locator('#vouchers-import-result')).toContainText('1 voucher(s) imported');
  });

  for (const c of CASES) {
    test(`Print slip: ${c.variant}`, async ({ page }) => {
      await stub(page, c.variant);
      await page.goto(`/vouchers/${CODE}?lang=en`);
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/print-slip') && r.status() === 200),
        page.getByTestId('voucher-print-slip').click(),
      ]);
      await expect(page.getByTestId('voucher-slip-browser-opened')).toBeVisible();
      await expect.poll(() => counts(page)).toEqual({ prints: c.prints, bridge: c.bridge });
    });
  }
});
