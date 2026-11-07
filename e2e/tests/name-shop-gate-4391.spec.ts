import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { markTourDone } from './helpers';

// ut-docs#4391: a manager on an unnamed MAIN till is asked for the shop and
// till name before an admin page opens -- never on the sale screen. Runs on
// an AUTH-ON worker till (the gate does not exist with UT_AUTH=off); the
// names are un-set through the settings API after sign-in.
test.describe.configure({ mode: 'serial' });

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

async function pinLogin(p: Page, pin: string) {
  await p.goto('/login');
  for (const d of pin.split('')) {
    await p.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await p.locator('button[type=submit].pin-key').click();
  await p.waitForURL((u) => !u.pathname.includes('/login'));
  await markTourDone(p);
}

test.beforeAll(async ({ browser }, workerInfo) => {
  till = await startWorkerTill(workerInfo.parallelIndex, { auth: true });
  context = await browser.newContext({ baseURL: till.url });
  page = await context.newPage();
  await pinLogin(page, till.adminPin!);
  for (const key of ['store.name', 'till.name']) {
    const res = await page.request.post('/api/settings/upsert', { form: { key, value: '' } });
    expect(res.ok(), `un-set ${key}`).toBe(true);
  }
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

test('an unnamed main till asks a manager for both names, never on the sale screen', async () => {
  await page.goto('/settings');
  await expect(page).toHaveURL(/\/name-shop\?next=%2Fsettings$/);
  await expect(page.getByTestId('name-shop-store')).toBeVisible();
  await expect(page.getByTestId('name-shop-store')).toHaveValue('');
  await expect(page.getByTestId('name-shop-till')).toHaveValue('');

  // The sale screen is never gated.
  await page.goto('/');
  await expect(page).toHaveURL(/\/$/);

  // A boosted rail navigation is gated too (R1): a full navigation, not a swap.
  await page.locator('nav a[href="/reports"]').first().click();
  await expect(page).toHaveURL(/\/name-shop/);

  // A blank submit is refused inline, nothing saved.
  await page.getByTestId('name-shop-save').click();
  await expect(page.getByTestId('name-shop-store-error')).toBeVisible();
  await expect(page.getByTestId('name-shop-till-error')).toBeVisible();

  await page.getByTestId('name-shop-store').fill('E2E Corner Bakery');
  await page.getByTestId('name-shop-till').fill('E2E Front counter');
  await page.getByTestId('name-shop-save').click();
  await expect(page).toHaveURL(/\/reports$/);

  // Named now: the gate is out of the way.
  await page.goto('/settings');
  await expect(page).toHaveURL(/\/settings$/);
});
