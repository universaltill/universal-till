import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#6332 / ADR-0190: shop type is a manager decision. A cashier is
// sale-only (ut-docs#3079): no shop-type card, no offer, no undo strip, no
// tucked-away note, and the read endpoints answer 403. Needs the `auth` project
// (real PIN sessions); named to sort AFTER login.spec.ts. REAL: the cashier
// PIN login. The admin sets the type through the real endpoint first.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier6332${RUN}`;
// Not 135792: the seeded kiosk user keeps that PIN (session-kiosk-pin-mode-3136) and a PIN-only
// login would resolve to the kiosk user (kiosk mode, no session) instead of this cashier.
const CASHIER_PIN = '580264';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 6332', role: 'cashier' },
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

test('a cashier sees no shop-type surface and cannot call its endpoints', async ({ page }) => {
  await ensureOperator(page); // admin
  await createCashier(page);

  // Admin sets a type: control that the surfaces DO exist for a manager.
  const set = await page.request.post('/api/settings/shop-type', { form: { to: 'retail' } });
  expect(set.ok(), 'admin sets the shop type').toBe(true);
  await page.goto('/settings#settings-shop-type');
  await expect(page.locator('#shop-type-change')).toBeVisible();
  await page.goto('/menu');
  await expect(page.locator('[data-testid="shop-type-undo"]')).toBeVisible();

  await loginAsCashier(page);
  await page.goto('/menu');
  await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Show all tiles' })).toHaveCount(0);
  await expect(page.locator('main')).not.toContainText('tucked away');

  const settings = await page.request.get('/settings');
  expect(settings.status(), 'a cashier cannot open Settings').toBe(403);
  for (const url of [
    '/api/settings/shop-type/panel',
    '/api/settings/shop-type/pick',
    '/api/settings/shop-type/preview?to=cafe',
  ]) {
    expect((await page.request.get(url)).status(), `GET ${url}`).toBe(403);
  }
  for (const url of [
    '/api/settings/shop-type/undo',
    '/api/settings/shop-type/dismiss-banner',
    '/api/settings/shop-type/offer/not-now',
    '/api/settings/shop-type/offer/never',
  ]) {
    expect((await page.request.post(url)).status(), `POST ${url}`).toBe(403);
  }
  // Confirm keeps the existing PIN-elevation prompt for a cashier (G7 note):
  // whatever it answers, it must not have changed the type.
  const confirm = await page.request.post('/api/settings/shop-type', { form: { to: 'cafe' } });
  expect(confirm.headers()['hx-redirect'], 'a cashier confirm must not apply the change').toBeUndefined();

  // Back as the admin: still retail.
  await page.request.post('/api/auth/logout');
  await ensureOperator(page);
  await page.goto('/settings');
  await expect(page.locator('[data-testid="settings-footer"]')).toContainText('Set up for: Retail shop');
});

// ut-docs#6348: the same wall for EVERY shipped type, not just retail. Admin and
// cashier are separate browser contexts (separate cookies), so the admin can
// switch the type between checks while the cashier session stays signed in.
// Ends on retail, the state the test above (and the rest of the auth project)
// expects.
const TYPES = ['cafe', 'hospitality', 'retail', 'service', 'market_stall', 'other'];

test('a cashier gets 403 on Settings and every shop-type endpoint in each of the six types', async ({ page, browser }) => {
  test.setTimeout(180_000);
  await ensureOperator(page); // admin
  // The first test creates the cashier; create it here only when run alone.
  if ((await page.request.get('/api/users')).ok() && !(await page.request.get('/users').then((r) => r.text())).includes(CASHIER_USERNAME)) {
    await createCashier(page);
  }
  const baseURL = test.info().project.use.baseURL;
  const ctx = await browser.newContext({ baseURL });
  const cashier = await ctx.newPage();
  try {
    await loginAsCashier(cashier);
    for (const type of TYPES) {
      const set = await page.request.post('/api/settings/shop-type', { form: { to: type } });
      expect(set.ok(), `admin sets ${type}`).toBe(true);

      expect((await cashier.request.get('/settings')).status(), `${type}: /settings`).toBe(403);
      for (const url of ['/api/settings/shop-type/panel', '/api/settings/shop-type/pick', '/api/settings/shop-type/preview?to=cafe']) {
        expect((await cashier.request.get(url)).status(), `${type}: GET ${url}`).toBe(403);
      }
      for (const url of ['/api/settings/shop-type/undo', '/api/settings/shop-type/dismiss-banner', '/api/settings/shop-type/offer/not-now', '/api/settings/shop-type/offer/never']) {
        expect((await cashier.request.post(url)).status(), `${type}: POST ${url}`).toBe(403);
      }
      const confirm = await cashier.request.post('/api/settings/shop-type', { form: { to: type === 'cafe' ? 'retail' : 'cafe' } });
      expect(confirm.headers()['hx-redirect'], `${type}: a cashier confirm must not apply a change`).toBeUndefined();

      // The cashier's Menu shows no shop-type surface, whatever the type.
      await cashier.goto('/menu');
      await expect(cashier.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
      await expect(cashier.getByRole('link', { name: 'Show all tiles' })).toHaveCount(0);
      await expect(cashier.locator('main')).not.toContainText('tucked away');
    }
  } finally {
    await ctx.close();
  }
  // Admin's last set was `other`; no cashier call changed it. Put retail back.
  expect((await page.request.post('/api/settings/shop-type', { form: { to: 'retail' } })).ok()).toBe(true);
  await page.goto('/settings');
  await expect(page.locator('[data-testid="settings-footer"]')).toContainText('Set up for: Retail shop');
});
