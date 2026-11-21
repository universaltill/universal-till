import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ADMIN_PIN, ensureOperator, markTourDone } from './helpers';

// ut-docs#5416 (regression of #1386): a cashier's New sale from the payment
// overlay is answered with the manager-PIN prompt; the approved retry is posted
// from the prompt's own form, so the overlay must be closed by app.js, not by
// the New sale button's data-after-request. Auth project; kiosk-size viewport.
test.use({ viewport: { width: 1024, height: 600 } });

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5416b${RUN}`;
const CASHIER_PIN = '246813';
const MANAGER_PIN = ADMIN_PIN;
let seq = 0; // the till is shared by the project's specs; one item per test

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5416b', role: 'cashier' },
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

async function setup(page: Page) {
  const SKU = `GATE5416b${RUN}${++seq}`;
  await ensureOperator(page);
  const itemResp = await page.request.post('/api/catalog/item', {
    form: { name: `Gate5416b Item ${SKU}`, price: '150', sku: SKU },
  });
  expect(itemResp.ok(), 'create catalog item').toBe(true);
  await createCashier(page);
  await loginAsCashier(page);
  // ut-docs#5416: New sale on a priced basket needs a manager PIN from a cashier.
  await page.request.post('/api/pos/reset', { form: { override_pin: MANAGER_PIN } });
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('.scan-row input[name="code"]').fill(SKU);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

test.describe('approved New sale closes the payment overlay (ut-docs#5416)', () => {
  test('manager PIN on New sale from the overlay clears the basket and closes it', async ({ page }) => {
    await setup(page);
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      page.getByTestId('kiosk-checkout-start').dispatchEvent('click'),
    ]);
    const dlg = page.locator('#elevation-modal');
    await expect(dlg).toHaveAttribute('open', '');
    // nothing cleared, overlay still open until a manager approves
    await expect(page.locator('#payment-overlay')).toBeVisible();

    await dlg.locator('input[name="override_pin"]').fill(MANAGER_PIN);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
      dlg.locator('button[type=submit]').click(),
    ]);
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(page.locator('#basket .qty-input')).toHaveCount(0);
  });
});
