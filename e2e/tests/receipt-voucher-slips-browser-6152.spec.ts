import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6152: with printer.voucher_slip on and no receipt printer, the
// browser-printed sale receipt carries each gift slip on its own page after
// the receipt (print-only). The setting is restored after every test.


// ut-docs#6155: every top-level @page rule the page carries (engine-neutral:
// CSSPageRule where exposed, else the rule text).
async function pageMarginRules(page) {
  return page.evaluate(() => {
    const out: string[] = [];
    for (const s of Array.from(document.styleSheets)) {
      let rules: CSSRule[] = [];
      try { rules = Array.from(s.cssRules); } catch { continue; }
      for (const r of rules) {
        if (r.cssText.startsWith('@page')) out.push(((r as any).style && (r as any).style.margin) || r.cssText);
      }
    }
    return out;
  });
}
function isZero(v: string) { return /^0(px)?$/.test(v.trim()) || /margin:\s*0(px)?\s*(;|\}|$)/.test(v); }

const RUN = Date.now().toString(36).toUpperCase();
const ITEM = `Gift voucher R6152 ${RUN}`;
const SKU = `GV-R6152-${RUN}`;
const TOPUP = `R6152T-${RUN}`;
let itemID = '';

async function setSlipSetting(page, on: boolean) {
  await page.goto('/settings#settings-printer');
  const box = page.getByTestId('printer-voucher-slip');
  if (on) await box.check(); else await box.uncheck();
  await page.locator('#settings-printer').getByRole('button', { name: 'Save printer' }).click();
  await page.reload();
  if (on) await expect(page.getByTestId('printer-voucher-slip')).toBeChecked();
  else await expect(page.getByTestId('printer-voucher-slip')).not.toBeChecked();
}

async function sell(page) {
  await page.request.post('/api/pos/reset');
  const scan = (code: string, value: string, holder: string) =>
    page.request.post('/api/pos/scan', { form: { code: SKU, gv: '1', gv_value_minor: value, gv_code: code, gv_holder: holder } });
  expect((await scan(`66${String(Date.now()).slice(-10)}1`, '3000', 'Spec holder')).ok()).toBeTruthy();
  expect((await scan(`66${String(Date.now()).slice(-10)}2`, '1000', '')).ok()).toBeTruthy();
  const up = await page.request.post(`/vouchers/${TOPUP}/top-up`, { form: { amount: '10.50' }, headers: { 'HX-Request': 'true' } });
  expect(up.ok(), `top-up: ${up.status()}`).toBeTruthy();
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.getByTestId('payment-open').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.locator('#payment-overlay').getByTestId('pay-default').click(),
  ]);
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
}

test.describe.serial('browser receipt carries the gift slips (ut-docs#6152)', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.post('/api/catalog/item', {
      form: { name: ITEM, price: '2500', sku: SKU, isActive: '1', itemType: 'gift_voucher' },
    });
    expect(res.ok(), `create item: ${res.status()}`).toBeTruthy();
    const m = /data-id="([^"]+)"/.exec(await res.text());
    itemID = m ? m[1] : '';
  });

  test.afterAll(async ({ request }) => {
    await request.post('/api/pos/reset');
    if (itemID) await request.post('/api/catalog/item/deactivate', { form: { id: itemID } });
  });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await setSlipSetting(page, false);
  });

  test('import the voucher to top up', async ({ page }) => {
    await page.goto('/vouchers?lang=en');
    await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
      name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${TOPUP},12.00,Top holder\n`),
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

  test('setting on: three slips, hidden on screen, one page each in print', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setSlipSetting(page, true);
    await sell(page);
    const slips = page.getByTestId('voucher-slip-paper');
    await expect(slips).toHaveCount(3);
    await expect(page.getByTestId('receipt-voucher-slips')).toHaveAttribute('aria-hidden', 'true');
    for (let i = 0; i < 3; i++) await expect(slips.nth(i)).toBeHidden();

    await page.emulateMedia({ media: 'print' });
    for (let i = 0; i < 3; i++) await expect(slips.nth(i)).toBeVisible();
    const breaks = await page.locator('.voucher-slip-sheet').evaluateAll((els) => els.map((e) => getComputedStyle(e).breakBefore));
    expect(breaks).toEqual(['page', 'page', 'page']);
    // ut-docs#6155: margin-0 page + the content's own 6mm inset (22.6px).
    const rules = await pageMarginRules(page);
    expect(rules.length, '@page rule on the receipt page').toBeGreaterThan(0);
    for (const r of rules) expect(isZero(r), `@page margin: ${r}`).toBeTruthy();
    const MM6 = 6 * 96 / 25.4 - 0.5;
    const inset = (sel: string) => page.locator(sel).first().evaluate((el) => {
      const s = getComputedStyle(el);
      return [s.paddingInlineStart, s.paddingInlineEnd, s.paddingBlockStart, s.paddingBlockEnd].map(parseFloat);
    });
    for (const sel of ['.receipt-wrap', '.voucher-slip-sheet']) {
      for (const v of await inset(sel)) expect(v, `${sel} print inset`).toBeGreaterThanOrEqual(MM6);
    }
    expect(await page.locator('.receipt-wrap').first().evaluate((el) => getComputedStyle(el).webkitBoxDecorationBreak || (getComputedStyle(el) as any).boxDecorationBreak)).toBe('clone');
    const w = await page.locator('.voucher-slip-paper').first().evaluate((el) => el.getBoundingClientRect().width);
    expect(w).toBeLessThanOrEqual(72 * 96 / 25.4 + 1);
    expect(await page.locator('.receipt-card:not(.receipt-print-copy)').first().evaluate((el) => el.getBoundingClientRect().width)).toBeLessThanOrEqual(421);
    // D1 (review): the slip sheets must not inherit the basket card frame
    // (border, radius, theme surface) on paper.
    const frame = await page.locator('#basket.receipt-view').evaluate((el) => {
      const s = getComputedStyle(el);
      return { border: s.borderTopWidth + ' ' + s.borderInlineStartWidth, radius: s.borderTopLeftRadius, bg: s.backgroundColor, pad: s.paddingInlineStart };
    });
    expect(frame).toEqual({ border: '0px 0px', radius: '0px', bg: 'rgb(255, 255, 255)', pad: '0px' });
    await expect(slips.nth(0)).toContainText('Spec holder');
    await expect(slips.nth(2)).toContainText('Gift voucher top-up');
    await expect(slips.nth(2)).toContainText('+');
    await expect(slips.nth(2)).toContainText('New balance');
    await expect(page.getByTestId('receipt-voucher-slips')).not.toContainText('Reprinted');
    assertClean();
  });

  test('setting off: no slip markup', async ({ page }) => {
    await setSlipSetting(page, false);
    await sell(page);
    await expect(page.getByTestId('voucher-slip-paper')).toHaveCount(0);
    await expect(page.getByTestId('receipt-voucher-slips')).toHaveCount(0);
  });
});
