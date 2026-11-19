import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5066 (ADR-0199, cashier is sale-only): restoring a cloud backup
// replaces the till's data. A manager/admin reaches the restore page; a
// cashier gets no control for it and every route refuses her on the server
// (hiding is only the second layer).
//
// Needs the `auth` project (a real PIN login). Named restore-… so it sorts
// AFTER login.spec.ts (it is in AUTH_ONLY_SPECS): the auth till is shared and serial. The auth till is not
// paired with a cloud, so the page shows its "only on a connected main till"
// line for the admin; what matters here is who is let in at all.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5066${RUN}`;
const CASHIER_PIN = '736419';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5066', role: 'cashier' },
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

test('a cashier gets no cloud-restore control and every route refuses her', async ({ page }) => {
  await ensureOperator(page); // admin

  // Control: the admin reaches the page.
  const adminPage = await page.request.get('/backups/cloud-restore');
  expect(adminPage.status(), 'admin reaches the cloud restore page').toBe(200);

  await createCashier(page);
  await loginAsCashier(page);

  await page.goto('/settings');
  await expect(page.locator('[data-testid="backup-cloud-card"]')).toHaveCount(0);
  await page.goto('/');
  await expect(page.locator('[data-testid="next-step-cloud-restore"]')).toHaveCount(0);

  expect((await page.request.get('/backups/cloud-restore')).status(), 'page refused').toBe(403);
  const id = '6f1ca001-1122-4344-8566-778899aabbcc';
  expect((await page.request.post('/api/offsite/recover/start', { form: { backup_id: id } })).status(), 'start refused').toBe(403);
  expect((await page.request.post('/api/offsite/recover/cancel')).status(), 'cancel refused').toBe(403);
  for (const p of ['/ui/backup-cloud-card', '/ui/backup-cloud-restore/status', '/ui/offsite-nextstep']) {
    const r = await page.request.get(p);
    expect((await r.text()).trim(), `${p} is empty for a cashier`).toBe('');
  }
  // The refused page still renders the rail, so Sell stays reachable.
  await page.goto('/backups/cloud-restore');
  await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
});
