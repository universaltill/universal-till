import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#6154: on a Pi kiosk with no OS printer the browser print dialog can
// only "Save as PDF" to a home nobody can reach, so UT.print shows an inline
// non-modal notice instead. The server sets <html data-browser-print="off">
// only on that kiosk (Go tests cover that); here the marker is set by hand on
// the always-rendered, hidden #ut-print-unavailable notice. window.print and
// the Android bridge are stubbed (no real dialog headless). Emulated: no real
// Pi touch hardware, no real printer.

const MARK = () => document.documentElement.setAttribute('data-browser-print', 'off');

async function stub(page: Page, bridge = false) {
  await page.addInitScript((b) => {
    const w = window as any;
    w.__prints = 0; w.__bridge = 0;
    w.print = () => { w.__prints++; };
    if (b) w.AndroidKiosk = { print() { w.__bridge++; return true; } };
  }, bridge);
}
const counts = (page: Page) => page.evaluate(() => ({ prints: (window as any).__prints, bridge: (window as any).__bridge }));

async function sellToReceipt(page: Page) {
  await page.request.post('/api/pos/reset');
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/?lang=en');
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

test.afterEach(async ({ page }) => {
  await page.request.post('/api/settings/invoice', { form: { seller_name: '', seller_address: '', seller_vat_no: '' } });
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

async function expectNotice(page: Page, shown: boolean) {
  const n = page.getByTestId('print-unavailable');
  if (!shown) { await expect(n).toBeHidden(); return; }
  await expect(n).toBeVisible();
  await expect(n).toHaveAttribute('role', 'status');
  await expect(n).toContainText('Nothing was printed. Check that the printer is set up and switched on.');
  expect(await page.locator('dialog[open]').count()).toBe(0);
  // status bar stays live: nothing is inert
  const inert = await page.getByTestId('status-indicator').evaluate((el) => !!el.closest('[inert]'));
  expect(inert).toBe(false);
}

test('invoice Print: marker -> notice, window.print 0 times', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.goto(href + '?lang=en');
  await page.evaluate(MARK);
  const btn = page.locator('[data-action="print"]').first();
  await btn.click();
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
  // focus is not moved by the notice
  await expect(btn).toBeFocused();
});

test('receipt Print after a failed printer job: marker -> notice', async ({ page }) => {
  await stub(page);
  await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 503, body: 'x' }));
  await sellToReceipt(page);
  await page.evaluate(MARK);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
    page.locator('[data-action^="receipt-print:"]').click(),
  ]);
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
  // honest copy: never claims there is no printer
  await expect(page.getByTestId('print-unavailable')).not.toContainText(/no printer/i);
});

test('receipt ask prompt Paper after a failed job: marker -> notice', async ({ page }) => {
  await stub(page);
  await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 503, body: 'x' }));
  await sellToReceipt(page);
  await page.evaluate(MARK);
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
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
});

test('no marker: window.print once, notice stays hidden', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.goto(href + '?lang=en');
  await page.locator('[data-action="print"]').first().click();
  expect((await counts(page)).prints).toBe(1);
  await expectNotice(page, false);
});

test('marker + Android bridge: bridge wins, no notice', async ({ page }) => {
  await stub(page, true);
  const { href } = await seedInvoice(page);
  await page.goto(href + '?lang=en');
  await page.evaluate(MARK);
  await page.locator('[data-action="print"]').first().click();
  expect(await counts(page)).toEqual({ prints: 0, bridge: 1 });
  await expectNotice(page, false);
});

test('dismiss hides the notice; the next Print shows it again', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.goto(href + '?lang=en');
  await page.evaluate(MARK);
  const btn = page.locator('[data-action="print"]').first();
  await btn.click();
  await expectNotice(page, true);
  await page.getByTestId('print-unavailable').locator('.notice-dismiss').click();
  await expectNotice(page, false);
  await btn.click();
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
});

test('fa at 360x740: notice does not overflow', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto(href + '?lang=fa');
  await page.evaluate(MARK);
  await page.locator('[data-action="print"]').first().click();
  await expect(page.getByTestId('print-unavailable')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  const box = await page.getByTestId('print-unavailable').locator('.notice-dismiss').boundingBox();
  expect(box!.width).toBeGreaterThanOrEqual(44);
  expect(box!.height).toBeGreaterThanOrEqual(44);
});

// D1: scheduleToastDismiss used to remove the hidden reusable notice after
// 2.5s, so a later Print found nothing to show.
test('notice survives the toast auto-dismiss: Print after 3.5s still shows it', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.goto(href + '?lang=en');
  await page.evaluate(MARK);
  await page.waitForTimeout(3500);
  await expect(page.getByTestId('print-unavailable')).toHaveCount(1);
  await page.locator('[data-action="print"]').first().click();
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
});

test('notice survives an htmx swap + 3.5s before Print', async ({ page }) => {
  await stub(page);
  await page.route('**/api/print/receipt/**', (r) => r.fulfill({ status: 503, body: 'x' }));
  await sellToReceipt(page);
  await page.evaluate(MARK);
  await page.waitForTimeout(3500);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/print/receipt/')),
    page.locator('[data-action^="receipt-print:"]').click(),
  ]);
  await expectNotice(page, true);
  expect((await counts(page)).prints).toBe(0);
});

// D2: at 360px the manager text must not be squeezed into a sliver.
test('en at 360x740: notice text and manager line stay readable', async ({ page }) => {
  await stub(page);
  const { href } = await seedInvoice(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto(href + '?lang=en');
  await page.evaluate(MARK);
  await page.locator('[data-action="print"]').first().click();
  const n = page.getByTestId('print-unavailable');
  await expect(n).toBeVisible();
  for (const t of await n.locator('.notice-text').all()) {
    expect((await t.boundingBox())!.width).toBeGreaterThanOrEqual(200);
  }
  expect((await n.boundingBox())!.height).toBeLessThan(150);
});
