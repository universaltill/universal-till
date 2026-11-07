import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, ADMIN_PIN, markTourDone, openPhoneSheet } from './helpers';

// ut-docs#3560: the sale screen's No Sale (open cash drawer) button. Posts
// POST /api/pos/no-sale through utPostWithElevation: a role holding
// cash_adjustment opens straight away; a cashier gets the shared manager-PIN
// dialog (non-modal), and Cancel sends nothing. The e2e till has no receipt
// printer, so the REAL server answers 409 no_drawer_printer (asserted inline
// in #pos-alert); the success path is stubbed with page.route. A real drawer
// kick cannot be checked here. Needs the `auth` project (cashier vs manager).

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier3560${RUN}`;
const CASHIER_PIN = '135790';
const NO_PRINTER = /no receipt printer is set up/i;

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 3560', role: 'cashier' },
  });
  expect(createResp.ok(), 'create cashier user').toBe(true);
  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];
  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginAsCashier(page: Page): Promise<void> {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of CASHIER_PIN.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
  await markTourDone(page);
}

function countNoSaleRequests(page: Page): () => number {
  let n = 0;
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/pos/no-sale') n++;
  });
  return () => n;
}

test.describe('Sale screen No Sale button (ut-docs#3560)', () => {
  test('manager: one tap, one request, no PIN dialog, the no-printer 409 shows inline', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    const requests = countNoSaleRequests(page);
    await page.getByTestId('no-sale').click();
    await expect(page.locator('#pos-alert')).toBeVisible();
    await expect(page.locator('#pos-alert .notice-text')).toHaveText(NO_PRINTER);
    await expect(page.locator('#elevation-modal[open]')).toHaveCount(0);
    expect(requests()).toBe(1);
    await expect(page.getByTestId('no-sale')).toBeEnabled();
  });

  test('success path (stubbed): notice shows, double click sends one request, button disabled in flight, basket untouched', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    const basketBefore = await page.locator('#basket').innerText();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    await page.route('**/api/pos/no-sale', async (route) => {
      await gate;
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { id: 'x' }, error: null }) });
    });
    const requests = countNoSaleRequests(page);
    const btn = page.getByTestId('no-sale');
    await btn.dblclick();
    await expect(btn).toBeDisabled();
    await expect(page.getByTestId('no-sale-phone')).toBeDisabled();
    release();
    await expect(page.locator('#no-sale-status')).toContainText('Cash drawer opened.');
    await expect(btn).toBeEnabled();
    expect(requests()).toBe(1);
    expect(await page.locator('#basket').innerText()).toBe(basketBefore);
    // Self-clearing (about 2.5 s).
    await expect(page.locator('#no-sale-status .pos-notice')).toHaveCount(0, { timeout: 6000 });
  });

  test('cashier: non-modal PIN dialog, wrong PIN, manager PIN, then Cancel sends nothing', async ({ page }) => {
    await ensureOperator(page);
    await createCashier(page);
    await loginAsCashier(page);
    await page.goto('/');
    const requests = countNoSaleRequests(page);
    const btn = page.getByTestId('no-sale');

    await btn.click();
    const dialog = page.locator('#elevation-modal');
    await expect(dialog).toBeVisible();
    expect(await dialog.evaluate((d) => d.matches(':modal'))).toBe(false);
    await expect(dialog).toContainText('Open the cash drawer without a sale');
    // Status bar / Lock stay reachable (no inert).
    expect(await page.evaluate(() => document.querySelectorAll('[inert]').length)).toBe(0);

    await dialog.locator('input[name="override_pin"]').fill('000000');
    await dialog.locator('button[type=submit]').click();
    await expect(dialog.locator('.login-error')).toBeVisible();

    await dialog.locator('input[name="override_pin"]').fill(ADMIN_PIN);
    await dialog.locator('button[type=submit]').click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('#pos-alert .notice-text')).toHaveText(NO_PRINTER);
    const afterPin = requests();
    expect(afterPin).toBe(3); // first tap, wrong PIN, right PIN

    // Cancel: nothing more is sent, no status, focus back on the button.
    await page.locator('#pos-alert .notice-dismiss').click();
    await btn.click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: /cancel/i }).click();
    await expect(dialog).toBeHidden();
    expect(requests()).toBe(afterPin + 1);
    await expect(page.locator('#pos-alert')).toBeHidden();
    await expect(page.locator('#no-sale-status .pos-notice')).toHaveCount(0);
    await expect(btn).toBeFocused();
  });

  test('phone width: the labelled copy is in the viewport; RTL smoke', async ({ page }) => {
    await ensureOperator(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/');
    await openPhoneSheet(page); // ut-docs#3059: the phone action row lives in the basket sheet
    const phone = page.getByTestId('no-sale-phone');
    await expect(phone).toBeVisible();
    await expect(phone).toBeInViewport();
    await expect(phone).toContainText('No Sale');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/?lang=fa');
    const row = page.getByTestId('no-sale');
    await expect(row).toBeVisible();
    const box = (await row.boundingBox())!;
    const pay = (await page.getByTestId('payment-open').boundingBox())!;
    expect(box.x).toBeLessThan(pay.x); // mirrored: No Sale is at the far inline-start
  });
});
