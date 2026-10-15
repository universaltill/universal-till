import { test, expect } from './fixtures';

// ut-docs#4393: /invoice/{no} had the same never-wrapping .page-head as
// /journal/{receipt} before ut-docs#4279 — title + Print / ← Receipt forced
// onto one line. Measured at 360px (en/fa/tr) it did not scroll sideways, but
// the title was squeezed into a ~120px column and the invoice number broke
// mid-token ("INV-" / "000001"). The header now wraps (page-head-wrap, as
// journal_detail.html): the action row drops below the title.

test.use({ viewport: { width: 360, height: 740 } });

// Configuring a seller identity is what turns invoicing on; turn it back
// off so later specs on this worker's till see the default (no invoices).
async function clearSeller(page: import('@playwright/test').Page) {
  await page.request.post('/api/settings/invoice', {
    form: { seller_name: '', seller_address: '', seller_vat_no: '' },
  });
}

test.afterEach(async ({ page }) => {
  await clearSeller(page);
  await page.request.post('/api/pos/reset');
});

test('invoice header fits a 360px phone, en, fa and tr (ut-docs#4393)', async ({ page }) => {
  const seller = await page.request.post('/api/settings/invoice', {
    form: {
      seller_name: 'Demo Shop Ltd',
      seller_address: '1 High Street, Demo Town',
      seller_vat_no: 'GB123456789',
    },
  });
  expect(seller.status(), 'configuring the invoice seller failed').toBe(204);

  await page.request.post('/api/pos/reset');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  const tender = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: { payments: [{ method: 'cash', amount: 100000 }] },
  });
  expect(tender.ok()).toBeTruthy();

  await page.goto('/journal');
  const href = await page.locator('a[href^="/journal/"]').first().getAttribute('href');
  expect(href).toBeTruthy();
  const receiptNo = href!.replace('/journal/', '');

  const issued = await page.request.post('/api/invoices/issue', {
    form: { receipt_no: receiptNo, customer_name: 'Jane Customer' },
  });
  expect(issued.ok(), await issued.text()).toBeTruthy();
  const invoiceHref = (await issued.text()).match(/href="(\/invoice\/[^"]+)"/)?.[1];
  expect(invoiceHref, 'issue response links the invoice').toBeTruthy();

  for (const lang of ['en', 'fa', 'tr']) {
    await page.goto(`${invoiceHref}?lang=${lang}`);
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    const btns = page.locator('.page-head .btn');
    expect(await btns.count()).toBe(2);
    await test.info().attach(`invoice-${lang}`, {
      body: await page.screenshot({ path: test.info().outputPath(`invoice-${lang}.png`) }),
      contentType: 'image/png',
    });
    const title = await page.locator('.page-head h1').boundingBox();
    const row = await btns.first().boundingBox();
    expect(row!.y, `${lang} action row sits below the title`).toBeGreaterThanOrEqual(title!.y + title!.height - 1);
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw, `${lang} scrollWidth`).toBeLessThanOrEqual(360);
    for (let i = 0; i < 2; i++) {
      const box = await btns.nth(i).boundingBox();
      expect(box!.x, `${lang} button ${i} left`).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width, `${lang} button ${i} right`).toBeLessThanOrEqual(360);
    }
  }
});
