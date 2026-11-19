import { test, expect } from './fixtures';

// ut-docs#6167 on a phone: the invoice page prints only the document, with
// @page margin 0 and its own inset. The CSSOM check is engine-neutral, so
// this runs in the WebKit lane too (phone-named). window.print is stubbed.

test.use({ viewport: { width: 360, height: 740 } });

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
const zeroMargin = (t: string) => /^0(px)?$/.test(t.trim()) || /margin:\s*0(px)?\s*(;|\}|$)/.test(t);

test.afterEach(async ({ page }) => {
  await page.request.post('/api/settings/invoice', { form: { seller_name: '', seller_address: '', seller_vat_no: '' } });
  await page.request.post('/api/pos/reset');
});

test('invoice page print CSS on a 360px phone (ut-docs#6167)', async ({ page }) => {
  const seller = await page.request.post('/api/settings/invoice', {
    form: { seller_name: 'Demo Shop Ltd', seller_address: '1 High Street, Demo Town', seller_vat_no: 'GB123456789' },
  });
  expect(seller.status()).toBe(204);
  await page.request.post('/api/pos/reset');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  const tender = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: { payments: [{ method: 'cash', amount: 100000 }] },
  });
  expect(tender.ok()).toBeTruthy();
  await page.goto('/journal');
  const jh = await page.locator('a[href^="/journal/"]').first().getAttribute('href');
  const issued = await page.request.post('/api/invoices/issue', {
    form: { receipt_no: jh!.replace('/journal/', ''), customer_name: 'Jane Customer' },
  });
  expect(issued.ok(), await issued.text()).toBeTruthy();
  const href = (await issued.text()).match(/href="(\/invoice\/[^"]+)"/)?.[1];
  expect(href).toBeTruthy();

  await page.addInitScript(() => { window.print = () => {}; });
  await page.goto(`${href}?lang=en`);

  const rules = await pageRules(page);
  expect(rules.length).toBeGreaterThan(0);
  for (const r of rules) expect(zeroMargin(r), r).toBeTruthy();

  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('.statusbar')).toBeHidden();
  await expect(page.locator('.invoice-actions')).toBeHidden();
  await expect(page.locator('#notices-strip')).toBeHidden();
  await expect(page.locator('.invoice-sheet')).toBeVisible();
  await expect(page.locator('.invoice-sheet')).toContainText('Jane Customer');
  const pad = await page.locator('.invoice-doc').evaluate((el) => parseFloat(getComputedStyle(el).paddingInlineStart));
  expect(pad).toBeGreaterThanOrEqual(15 * 96 / 25.4 - 0.5);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
});
