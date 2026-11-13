import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5288, PIN caller: while the manager-PIN dialog's OWN request is in
// flight (the retry, which is not idempotent), Cancel and Escape do nothing
// and Cancel is disabled; the PIN pad's keyboard toggle stays enabled. When
// the answer arrives the dialog closes with it shown. The held request is
// fulfilled by the test, so nothing is changed on the till.
//
// Needs the `auth` project (a cashier without catalog_management gets the
// real elevation prompt; auth-off never asks). Sorts after login.spec.ts.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5288${RUN}`;
const CASHIER_PIN = '246813';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5288', role: 'cashier' },
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

test.describe('PIN prompt in flight: Cancel and Escape are inert (ut-docs#5288)', () => {
  test('Escape and Cancel mid-request keep the dialog open; the answer then shows', async ({ page }) => {
    await ensureOperator(page);
    await createCashier(page);
    await loginAsCashier(page);
    await page.goto('/');

    let release!: () => void;
    const gate = new Promise<void>((r) => { release = r; });
    let held!: () => void;
    const inFlight = new Promise<void>((r) => { held = r; });
    let posts = 0;
    await page.route('**/api/buttons/recategorize', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      posts++;
      if (posts === 1) return route.continue(); // raises the real PIN prompt
      held();
      await gate;
      return route.fulfill({ status: 200, contentType: 'text/html', body: '<span id="pin-answer-5288">Answer 5288</span>' });
    });

    // A cashier lacks catalog_management: this raises the real prompt. The
    // handler names #buttons-add-error as the retry's target and the sale page
    // has none, so the test supplies it (htmx drops a request whose target is
    // missing).
    await page.evaluate(() => {
      const t = document.createElement('div');
      t.id = 'buttons-add-error';
      document.body.appendChild(t);
      (window as any).htmx.ajax('POST', '/api/buttons/recategorize', { target: '#buttons-add-error', swap: 'innerHTML', values: { item_id: 'x', category_id: '' } });
    });
    const dlg = page.locator('#elevation-modal');
    await expect(dlg).toHaveJSProperty('open', true);
    const cancel = dlg.locator('[data-action="close-closest"]');
    const osk = dlg.locator('[data-osk-toggle]');
    await dlg.locator('input[name="override_pin"]').fill('123456');
    await dlg.locator('button[type=submit]').click();
    await inFlight;
    await expect(cancel).toBeDisabled();
    await expect(dlg.locator('button[type=submit]')).toBeDisabled();
    await expect(osk).toBeEnabled();
    // ut-docs#5291: busy state on the htmx-driven prompt too.
    await expect(dlg.locator('form')).toHaveAttribute('aria-busy', 'true');
    expect(await dlg.locator('button[type=submit]').evaluate((el) => getComputedStyle(el, '::after').content)).not.toBe('none');

    await page.keyboard.press('Escape');
    await cancel.click({ force: true });
    await expect(dlg).toHaveJSProperty('open', true);
    expect(posts).toBe(2);

    release();
    await expect(page.locator('#pin-answer-5288')).toHaveText('Answer 5288');
    await expect(page.locator('#elevation-modal form[aria-busy="true"]')).toHaveCount(0);
    await expect(dlg).toHaveJSProperty('open', false);
  });

  // ut-docs#5298: the htmx PIN prompt's retry dropping at network level says
  // the action may not have completed, in #pos-alert.
  test('a network failure on the PIN retry shows the outcome-unknown alert', async ({ page }) => {
    await ensureOperator(page);
    await createCashier(page);
    await loginAsCashier(page);
    await page.goto('/');
    let posts = 0;
    await page.route('**/api/buttons/recategorize', (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      posts++;
      return posts === 1 ? route.continue() : route.abort('failed');
    });
    await page.evaluate(() => {
      const t = document.createElement('div');
      t.id = 'buttons-add-error';
      document.body.appendChild(t);
      (window as any).htmx.ajax('POST', '/api/buttons/recategorize', { target: '#buttons-add-error', swap: 'innerHTML', values: { item_id: 'x', category_id: '' } });
    });
    const dlg = page.locator('#elevation-modal');
    await expect(dlg).toHaveJSProperty('open', true);
    await dlg.locator('input[name="override_pin"]').fill('123456');
    await dlg.locator('button[type=submit]').click();
    await expect(dlg).toHaveJSProperty('open', false);
    await expect(page.locator('#pos-alert .notice-text')).toHaveText('The connection dropped before the till replied, so this may or may not have been done. Check before you try again.');
    expect(posts).toBe(2);
  });
});
