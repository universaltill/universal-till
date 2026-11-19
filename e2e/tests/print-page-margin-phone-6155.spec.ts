import { test, expect } from './fixtures';

// ut-docs#6155: browser-printed receipts and voucher slips carry
// @page { margin: 0 } so the browser has no margin box for its URL / title /
// date header and footer, and the content supplies its own inset. The CSSOM
// check is engine-neutral, so this runs in the WebKit lane too (phone-named).
// No printer or print dialog is driven: window.print is stubbed.

test.use({ viewport: { width: 360, height: 740 } });

const RUN = Date.now().toString(36).toUpperCase();
const CODE = `T6155-${RUN}`;

async function pageRules(page) {
  return page.evaluate(() => {
    const out: string[] = [];
    for (const s of Array.from(document.styleSheets)) {
      let rules: CSSRule[] = [];
      try { rules = Array.from(s.cssRules); } catch { continue; }
      for (const r of rules) if (r.cssText.startsWith('@page')) out.push(((r as any).style && (r as any).style.margin) || r.cssText);
    }
    return out;
  });
}
// Anchored after the 0: `margin: 0 20mm` must fail (review F1).
const zeroMargin = (t: string) => /^0(px)?$/.test(t.trim()) || /margin:\s*0(px)?\s*(;|\}|$)/.test(t);

test.afterEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });

test('receipt page carries @page margin 0 and an inset (ut-docs#6155)', async ({ page }) => {
  await page.request.post('/api/pos/reset');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('.basket-phonebar').click();
  await page.getByTestId('payment-open').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.locator('#payment-overlay').getByTestId('pay-default').click(),
  ]);
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  const rules = await pageRules(page);
  expect(rules.length).toBeGreaterThan(0);
  for (const r of rules) expect(zeroMargin(r), r).toBeTruthy();
  await page.emulateMedia({ media: 'print' });
  const pad = await page.locator('.receipt-wrap').evaluate((el) => parseFloat(getComputedStyle(el).paddingInlineStart));
  expect(pad).toBeGreaterThanOrEqual(6 * 96 / 25.4 - 0.5);
});

test('voucher page gets @page margin 0 only once a slip is requested (ut-docs#6155)', async ({ page }) => {
  await page.addInitScript(() => { window.print = () => {}; window.open = () => null; });
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
  await page.goto(`/vouchers/${CODE}?lang=en`);
  expect(await pageRules(page)).toEqual([]);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/print-slip') && r.status() === 200),
    page.getByTestId('voucher-print-slip').click(),
  ]);
  const rules = await pageRules(page);
  expect(rules.length).toBeGreaterThan(0);
  for (const r of rules) expect(zeroMargin(r), r).toBeTruthy();
  await page.emulateMedia({ media: 'print' });
  const pad = await page.locator('#voucher-slip-print').evaluate((el) => parseFloat(getComputedStyle(el).paddingInlineStart));
  expect(pad).toBeGreaterThanOrEqual(6 * 96 / 25.4 - 0.5);
});
