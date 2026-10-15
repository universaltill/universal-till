import { test, expect } from './fixtures';

// ut-docs#4402: the invoice register (/invoices) had the same never-wrapping
// .page-head as /invoice/{no} before ut-docs#4393 — title + Export CSV /
// ← Journal forced onto one line, so at a 360px phone the h1 was squeezed
// into a narrow column and broke mid-word. The header now wraps
// (page-head-wrap, as journal_detail.html / invoice.html): the action row
// drops below the title.

test.use({ viewport: { width: 360, height: 740 } });

// Configuring a seller identity is what turns invoicing on (and with it the
// register); turn it back off so later specs on this worker's till see the
// default (no invoices).
async function clearSeller(page: import('@playwright/test').Page) {
  await page.request.post('/api/settings/invoice', {
    form: { seller_name: '', seller_address: '', seller_vat_no: '' },
  });
}

test.afterEach(async ({ page }) => {
  await clearSeller(page);
  await page.request.post('/api/pos/reset');
});

test('invoice register header fits a 360px phone, en, fa and tr (ut-docs#4402)', async ({ page }) => {
  const seller = await page.request.post('/api/settings/invoice', {
    form: {
      seller_name: 'Demo Shop Ltd',
      seller_address: '1 High Street, Demo Town',
      seller_vat_no: 'GB123456789',
    },
  });
  expect(seller.status(), 'configuring the invoice seller failed').toBe(204);

  // One issued invoice so the register renders its table, not just the
  // empty state — the header must fit with a populated page too.
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
  const issued = await page.request.post('/api/invoices/issue', {
    form: { receipt_no: href!.replace('/journal/', ''), customer_name: 'Jane Customer' },
  });
  expect(issued.ok(), await issued.text()).toBeTruthy();

  for (const lang of ['en', 'fa', 'tr']) {
    await page.goto(`/invoices?lang=${lang}`);
    await expect(page).toHaveURL(/\/invoices/);
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    const btns = page.locator('.page-head .btn');
    expect(await btns.count()).toBe(2);
    await expect(page.locator('table.table tbody tr').first()).toBeVisible();
    await test.info().attach(`invoices-${lang}`, {
      body: await page.screenshot({ path: test.info().outputPath(`invoices-${lang}.png`) }),
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
