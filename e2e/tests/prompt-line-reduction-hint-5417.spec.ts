import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ADMIN_PIN, ensureOperator, markTourDone } from './helpers';

// ut-docs#5417 (follow-up to #5411): a cashier lowering a priced line's qty
// gets the manager-PIN prompt. The basket resync that restores the real qty
// in the box used to wipe the prompt's #shrinkage-hint; the hint must survive
// it, Cancel must still remove it, and an approval must leave none behind.
// Auth project (a cashier gets the real prompt); the admin created by
// ensureOperator is the approver. Kiosk-size viewport.
test.use({ viewport: { width: 1024, height: 600 } });

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5417${RUN}`;
const CASHIER_PIN = '246813';
const MANAGER_PIN = ADMIN_PIN;
let seq = 0; // the till is shared by the project's specs; one item per test
const HINT_TEXT = 'Manager approval needed';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5417', role: 'cashier' },
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
  const SKU = `HINT5417${RUN}${++seq}`;
  await ensureOperator(page);
  const itemResp = await page.request.post('/api/catalog/item', {
    form: { name: `Hint5417 Item ${SKU}`, price: '150', sku: SKU },
  });
  expect(itemResp.ok(), 'create catalog item').toBe(true);
  await createCashier(page);
  await loginAsCashier(page);
  await page.request.post('/api/pos/reset');
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('.scan-row input[name="code"]').fill(SKU);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  // qty 3 (the stepper posts a relative delta; + is ungated)
  for (let i = 0; i < 2; i++) {
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/line')),
      page.locator('#basket .qty-step-btn').nth(1).click(),
    ]);
  }
  await expect(page.locator('#basket .qty-input').first()).toHaveValue('3');
}

// Type 1 in the qty box and wait for the prompt plus the resync that follows.
async function lowerQtyToOne(page: Page) {
  const qty = page.locator('#basket .qty-input').first();
  await qty.fill('1');
  const resync = page.waitForResponse((r) => r.url().includes('/ui/basket'));
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/line')),
    qty.press('Tab'),
  ]);
  await resync;
  await page.waitForLoadState('networkidle');
}

test.describe('line reduction hint survives the resync (ut-docs#5417)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('hint stays, qty box is restored, Cancel clears the hint', async ({ page }) => {
    await setup(page);
    await lowerQtyToOne(page);
    const dlg = page.locator('#elevation-modal');
    await expect(dlg).toHaveAttribute('open', '');
    const hint = page.locator('#shrinkage-hint');
    await expect(hint).toContainText(HINT_TEXT);
    // shown exactly once
    await expect(hint.getByText(HINT_TEXT)).toHaveCount(1);
    await expect(page.locator('#basket .qty-input').first()).toHaveValue('3');
    await page.screenshot({ path: test.info().outputPath('hint-after-resync.png') });

    await dlg.locator('button', { hasText: 'Cancel' }).first().click();
    await expect(dlg).not.toHaveAttribute('open', '');
    await expect(hint).toHaveText('');
    await expect(page.locator('#basket .qty-input').first()).toHaveValue('3');
  });

  test('manager PIN applies the reduction and leaves no hint', async ({ page }) => {
    await setup(page);
    await lowerQtyToOne(page);
    const dlg = page.locator('#elevation-modal');
    await expect(page.locator('#shrinkage-hint')).toContainText(HINT_TEXT);
    await dlg.locator('input[name="override_pin"]').fill(MANAGER_PIN);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/line')),
      dlg.locator('button[type=submit]').click(),
    ]);
    await page.waitForLoadState('networkidle');
    await expect(page.locator('#basket .qty-input').first()).toHaveValue('1');
    await expect(page.locator('#shrinkage-hint')).toHaveText('');
    await expect(dlg).not.toHaveAttribute('open', '');
  });

  test('remove sheet flow is unchanged: prompt with its hint', async ({ page }) => {
    await setup(page);
    await page.locator('#basket .shrinkage-remove-toggle').first().click();
    const sheet = page.locator('.shrinkage-sheet[open]').first();
    await expect(sheet).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/remove')),
      sheet.locator('button.danger').first().click(),
    ]);
    await expect(page.locator('#elevation-modal')).toHaveAttribute('open', '');
    await expect(page.locator('#shrinkage-hint')).toContainText(HINT_TEXT);
  });
});
