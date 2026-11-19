import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6152: "Print voucher slip" on /vouchers/{id} with no receipt
// printer opens the browser's print dialog on the same page with just the
// slip. The e2e till has no printer. window.print / window.open are stubbed
// (a real dialog can't be driven headless); the print-media layout is
// checked with emulateMedia. The shadow-market refusal is covered by Go
// (the e2e till can't run a suppressed market), here only its client side.


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
const CODE = `T6152-${RUN}`;

test.describe.serial('voucher slip through the browser (ut-docs#6152)', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      (window as any).__prints = 0;
      (window as any).__opens = 0;
      window.print = () => { (window as any).__prints++; };
      window.open = () => { (window as any).__opens++; return null; };
    });
  });

  test('import a voucher (25.00, Spec holder)', async ({ page }) => {
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

  test('the press opens the print dialog once with only the slip', async ({ page, context }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto(`/vouchers/${CODE}?lang=en`);
    await expect(page.getByTestId('voucher-slip-browser-hint')).toBeVisible();
    const hintLink = page.getByTestId('voucher-slip-browser-hint').locator('a');
    expect((await hintLink.boundingBox())!.height).toBeGreaterThanOrEqual(43);
    // ut-docs#6155: no @page rule on the plain voucher page (no spill).
    expect(await pageMarginRules(page)).toEqual([]);

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/print-slip') && r.status() === 200),
      page.getByTestId('voucher-print-slip').click(),
    ]);
    const status = page.getByTestId('voucher-slip-browser-opened');
    await expect(status).toHaveText('Print dialog opened for this slip.');
    await expect(status).not.toHaveClass(/\bok\b/);
    expect(await page.evaluate(() => (window as any).__prints)).toBe(1);
    expect(await page.evaluate(() => (window as any).__opens)).toBe(0);
    expect(context.pages().length).toBe(1);
    // On screen the slip is not shown.
    await expect(page.locator('#voucher-slip-print .voucher-slip-paper')).toBeHidden();

    const rules = await pageMarginRules(page);
    expect(rules.length, '@page rule after the press').toBeGreaterThan(0);
    for (const r of rules) expect(isZero(r), `@page margin: ${r}`).toBeTruthy();

    await page.emulateMedia({ media: 'print' });
    // The host padding must survive the `padding:0 !important` ancestor rule.
    const MM6 = 6 * 96 / 25.4 - 0.5;
    const pads = await page.locator('#voucher-slip-print').evaluate((el) => {
      const s = getComputedStyle(el);
      return [s.paddingInlineStart, s.paddingInlineEnd, s.paddingBlockStart, s.paddingBlockEnd].map(parseFloat);
    });
    for (const v of pads) expect(v, 'host print inset').toBeGreaterThanOrEqual(MM6);
    const slips = page.getByTestId('voucher-slip-paper');
    await expect(slips).toHaveCount(1);
    await expect(slips).toBeVisible();
    await expect(slips).toContainText('Gift voucher');
    await expect(slips).toContainText('25.00');
    await expect(slips).toContainText('Spec holder');
    await expect(slips).toContainText(CODE);
    await expect(slips).toContainText('Reprinted');
    await expect(slips.locator('svg')).toHaveCount(1);
    for (const sel of ['nav', '.voucher-facts', '#voucher-adjust', '#voucher-top-up', '[data-testid=voucher-print-slip]']) {
      await expect(page.locator(sel).first(), `${sel} hidden in print`).toBeHidden();
    }
    assertClean();
  });

  test('a refusal (451) opens no dialog and shows no slip', async ({ page }) => {
    await page.route('**/vouchers/*/print-slip', (r) =>
      r.fulfill({ status: 451, contentType: 'text/html', body: '<p data-testid="shadow-documents-refused">x</p>' }));
    await page.goto(`/vouchers/${CODE}?lang=en`);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/print-slip')),
      page.getByTestId('voucher-print-slip').click(),
    ]);
    expect(await page.evaluate(() => (window as any).__prints)).toBe(0);
    await expect(page.locator('#voucher-slip-print')).toBeEmpty();
  });

  test('phone width in fa: no sideways scroll with the hint', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(`/vouchers/${CODE}?lang=fa`);
    await expect(page.getByTestId('voucher-slip-browser-hint')).toBeVisible();
    const { sw, vw } = await page.evaluate(() => ({
      sw: document.documentElement.scrollWidth, vw: document.documentElement.clientWidth,
    }));
    expect(sw).toBeLessThanOrEqual(vw + 1);
  });
});
