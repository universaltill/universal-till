import { test, expect } from './fixtures';

// ut-docs#5749: an HTMX request that disables the control the operator is on
// (hx-disabled-elt) drops focus to <body>, so a keyboard user loses their
// place. app.js puts focus back on the control once htmx re-enables it, or on
// the request's live-region target if the control is gone, and never steals
// focus the operator moved on purpose.

const RUN = Date.now().toString(36).toUpperCase();
const CODE = `T5749-${RUN}`;

const activeIsBody = () =>
  document.activeElement === document.body || document.activeElement === null;

// Each test seeds the voucher when its till doesn't have it yet (a failed
// test restarts the worker on a fresh till), so no test depends on another.
async function ensureVoucher(page: import('@playwright/test').Page) {
  const r = await page.request.get(`/vouchers/${CODE}?lang=en`);
  if (r.ok()) return;
  await page.goto('/vouchers?lang=en');
  await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
    name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${CODE},12.00,Spec holder\n`),
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
}

test.describe('focus after an hx-disabled-elt request (ut-docs#5749)', () => {
  test.beforeEach(async ({ page }) => { await ensureVoucher(page); });

  test('adjust form: a refusal (400) keeps focus on the submit button', async ({ page }) => {
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const sec = page.locator('#voucher-adjust');
    // Skip the browser's pattern check so the server's 400 comes back inline.
    await sec.locator('form').evaluate((f: HTMLFormElement) => { f.noValidate = true; });
    await sec.locator('input[name=new_balance]').fill('abc');
    await sec.locator('input[name=reason]').fill('spec refusal');
    const submit = sec.locator('button[type=submit]');
    await submit.focus();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/adjust') && r.status() === 400),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#voucher-adjust-msg')).not.toBeEmpty();
    await expect(submit).toBeEnabled();
    await expect(submit).toBeFocused();
    expect(await page.evaluate(activeIsBody)).toBe(false);
  });

  test('top-up form: a refusal (400) keeps focus on the submit button', async ({ page }) => {
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const sec = page.locator('#voucher-top-up');
    await sec.locator('input[name=amount]').fill('0');
    const submit = sec.locator('button[type=submit]');
    await submit.focus();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/top-up') && r.status() === 400),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#voucher-top-up-msg [role=alert]')).toContainText('Enter an amount above zero');
    await expect(submit).toBeEnabled();
    await expect(submit).toBeFocused();
  });

  test('moved on during the request: focus is not stolen', async ({ page }) => {
    await page.route('**/top-up', async (route) => {
      await new Promise((r) => setTimeout(r, 500));
      await route.continue();
    });
    await page.goto(`/vouchers/${CODE}?lang=en`);
    const sec = page.locator('#voucher-top-up');
    const amount = sec.locator('input[name=amount]');
    await amount.fill('0');
    const submit = sec.locator('button[type=submit]');
    await submit.focus();
    const resp = page.waitForResponse((r) => r.url().includes('/top-up') && r.status() === 400);
    await page.keyboard.press('Enter');
    await expect(submit).toBeDisabled();
    await amount.focus();
    await resp;
    await expect(page.locator('#voucher-top-up-msg [role=alert]')).toBeVisible();
    await expect(submit).toBeEnabled();
    await expect(amount).toBeFocused();
  });

  test('control swapped away: the live-region target takes focus', async ({ page }) => {
    await page.route('**/__fb-5749', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<p>Done</p>' }));
    await page.goto(`/vouchers/${CODE}?lang=en`);
    await page.evaluate(() => {
      const box = document.createElement('div');
      box.innerHTML =
        '<div id="fb-live" aria-live="polite"><form id="fb-form" hx-post="/__fb-5749" hx-target="#fb-live" hx-swap="innerHTML" hx-disabled-elt="find button[type=submit]"><button id="fb-btn" type="submit">Go</button></form></div>';
      document.body.appendChild(box);
      (window as any).htmx.process(box);
    });
    await page.locator('#fb-btn').focus();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/__fb-5749')),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#fb-live')).toContainText('Done');
    await expect(page.locator('#fb-live')).toBeFocused();
    await expect(page.locator('#fb-live')).toHaveAttribute('tabindex', '-1');
  });

  test('outerHTML swap that replaces its own target: the new region takes focus', async ({ page }) => {
    await page.route('**/__fb2-5749', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<div id="fb2-box" role="status"><p>Done</p></div>' }));
    await page.goto(`/vouchers/${CODE}?lang=en`);
    await page.evaluate(() => {
      const box = document.createElement('div');
      box.innerHTML =
        '<div id="fb2-box"><button id="fb2-btn" type="button" hx-post="/__fb2-5749" hx-target="#fb2-box" hx-swap="outerHTML" hx-disabled-elt="this">Go</button></div>';
      document.body.appendChild(box);
      (window as any).htmx.process(box);
    });
    await page.locator('#fb2-btn').focus();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/__fb2-5749')),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#fb2-box')).toContainText('Done');
    await expect(page.locator('#fb2-box')).toBeFocused();
    // The added tabindex goes once focus leaves, so a later click inside the
    // region doesn't keep focusing the container.
    await page.keyboard.press('Tab');
    await expect(page.locator('#fb2-box')).not.toHaveAttribute('tabindex', /.*/);
  });
});
