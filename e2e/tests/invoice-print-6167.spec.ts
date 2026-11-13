import { test, expect } from './fixtures';

// ut-docs#6167: the invoice page printed the whole app shell (rail, status
// bar, buttons) with the browser's default margins and URL/date header and
// footer. It now carries @page { margin: 0 }, hides everything but the
// document under print media, and supplies its own 15mm inset.
// No printer is driven: print media is emulated and window.print is stubbed.

test.use({ viewport: { width: 794, height: 1123 } }); // ~A4 at 96dpi

const MIN_INSET = 15 * 96 / 25.4 - 0.5;

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

async function clearSeller(page) {
  await page.request.post('/api/settings/invoice', {
    form: { seller_name: '', seller_address: '', seller_vat_no: '' },
  });
}

test.afterEach(async ({ page }) => {
  await clearSeller(page);
  await page.request.post('/api/pos/reset');
});

async function seedInvoice(page): Promise<{ href: string; displayNo: string }> {
  const seller = await page.request.post('/api/settings/invoice', {
    form: { seller_name: 'Demo Shop Ltd', seller_address: '1 High Street, Demo Town', seller_vat_no: 'GB123456789' },
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
  const jh = await page.locator('a[href^="/journal/"]').first().getAttribute('href');
  expect(jh).toBeTruthy();
  const issued = await page.request.post('/api/invoices/issue', {
    form: { receipt_no: jh!.replace('/journal/', ''), customer_name: 'Jane Customer' },
  });
  expect(issued.ok(), await issued.text()).toBeTruthy();
  const href = (await issued.text()).match(/href="(\/invoice\/[^"]+)"/)?.[1];
  expect(href, 'issue response links the invoice').toBeTruthy();
  return { href: href!, displayNo: decodeURIComponent(href!.replace('/invoice/', '')) };
}

test('invoice page prints only the document (ut-docs#6167)', async ({ page, browserName }) => {
  const { href, displayNo } = await seedInvoice(page);
  await page.addInitScript(() => {
    (window as any).__prints = 0;
    window.print = () => { (window as any).__prints++; };
  });
  await page.goto(`${href}?lang=en`);

  // Screen view keeps its chrome; Print still calls window.print once.
  await expect(page.locator('.invoice-actions .btn')).toHaveCount(2);
  await expect(page.locator('.invoice-actions .btn').first()).toBeVisible();
  await expect(page.locator('.nav').first()).toBeVisible();
  await expect(page.locator('.statusbar')).toBeVisible();
  await page.locator('.invoice-actions [data-action="print"]').click();
  expect(await page.evaluate(() => (window as any).__prints)).toBe(1);

  const rules = await pageRules(page);
  expect(rules.length).toBeGreaterThan(0);
  for (const r of rules) expect(zeroMargin(r), r).toBeTruthy();

  for (const lang of ['en', 'fa']) {
    await page.emulateMedia({ media: 'screen' });
    await page.goto(`${href}?lang=${lang}`);
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    await page.emulateMedia({ media: 'print' });

    await expect(page.locator('.nav').first()).toBeHidden();
    await expect(page.locator('.statusbar')).toBeHidden();
    await expect(page.locator('.invoice-actions')).toBeHidden();
    await expect(page.locator('#notices-strip')).toBeHidden();
    await expect(page.locator('.invoice-doc h1')).toBeVisible();
    await expect(page.locator('.invoice-doc h1')).toContainText(displayNo);
    await expect(page.locator('.invoice-sheet')).toBeVisible();
    await expect(page.locator('.invoice-sheet')).toContainText(displayNo);
    await expect(page.locator('.invoice-sheet')).toContainText('Jane Customer');

    const pads = await page.locator('.invoice-doc').evaluate((el) => {
      const cs = getComputedStyle(el);
      return [cs.paddingInlineStart, cs.paddingInlineEnd, cs.paddingBlockStart, cs.paddingBlockEnd].map(parseFloat);
    });
    for (const p of pads) expect(p, `${lang} inset`).toBeGreaterThanOrEqual(MIN_INSET);

    const box = await page.locator('.invoice-doc').boundingBox();
    expect(box!.x, `${lang} starts at page edge`).toBeLessThanOrEqual(1);
    expect(box!.width, `${lang} full width`).toBeGreaterThanOrEqual(793);
    expect(await page.evaluate(() => document.documentElement.scrollWidth), `${lang} scrollWidth`).toBeLessThanOrEqual(794);
    expect(await page.locator('.invoice-sheet h3').evaluate((el) => getComputedStyle(el).color)).toBe('rgb(0, 0, 0)');
    expect(await page.locator('.invoice-sheet').evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgb(255, 255, 255)');
    // A line's unit-price sub-row never lands alone at the top of the next page.
    expect(await page.locator('.invoice-sheet tbody tr:has(+ tr.muted)').first().evaluate((el) => getComputedStyle(el).breakAfter)).toBe('avoid');

    await test.info().attach(`invoice-print-${lang}`, {
      body: await page.screenshot({ path: test.info().outputPath(`invoice-print-${lang}.png`), fullPage: true }),
      contentType: 'image/png',
    });
    if (browserName === 'chromium' && lang === 'en') {
      await test.info().attach('invoice-a4.pdf', {
        body: await page.pdf({ format: 'A4' }),
        contentType: 'application/pdf',
      });
    }
  }

  // The @page rule leaves with the page: a boosted nav away keeps none.
  await page.emulateMedia({ media: 'screen' });
  await page.goto(`${href}?lang=en`);
  await page.locator('.invoice-actions a.btn').click();
  await page.waitForURL(/\/journal\//);
  expect(await pageRules(page)).toEqual([]);
});
