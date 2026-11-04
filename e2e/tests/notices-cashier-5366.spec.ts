import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5366 (ADR-0176 §7, cashier is sale-only): with notices switched on,
// a manager sees the bell and the inbox; a cashier without the notices.view
// permission sees NO bell, and the inbox route and every notice write answer
// 403 server-side (hiding the control is only the second layer).
//
// Needs the `auth` project (a real PIN login). Named to sort AFTER
// login.spec.ts: the auth till is shared and serial.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5366${RUN}`;
const CASHIER_PIN = '135792';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5366', role: 'cashier' },
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

test('a cashier gets no bell and no inbox, refused on the server', async ({ page }) => {
  await ensureOperator(page); // admin
  const on = await page.request.post('/api/settings/notices', { form: { enabled: 'on' } });
  expect(on.status(), 'admin turns notices on').toBe(204);

  // Control: the admin has the bell and the inbox page (empty state).
  await page.goto('/');
  await expect(page.locator('[data-testid="notices-bell"]')).toBeVisible();
  await page.goto('/notices');
  await expect(page.locator('[data-testid="notices-empty"]')).toBeVisible();

  await createCashier(page);
  await loginAsCashier(page);

  await page.goto('/');
  await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
  await expect(page.locator('[data-testid="notices-bell"]')).toHaveCount(0);

  const inbox = await page.request.get('/notices');
  expect(inbox.status(), 'inbox route refused for a cashier').toBe(403);
  for (const p of ['/notices/x/read', '/notices/x/dismiss', '/notices/x/ack']) {
    expect((await page.request.post(p)).status(), `${p} refused`).toBe(403);
  }
  expect((await page.request.post('/api/settings/notices', { form: { enabled: 'on' } })).status()).toBe(403);
  // The refused page still renders the rail, so Sell stays reachable.
  await page.goto('/notices');
  await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
});
