import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5335, PIN callers (auth project; a cashier gets the real prompt):
// the PIN submit's request, htmx-driven (A1) or utPostWithElevation's fetch
// retry (B2), is abandoned after the (overridden, 1 s) timeout and handled as
// #5298's network failure. Sorts after login.spec.ts.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5335${RUN}`;
const CASHIER_PIN = '246813';
const OUTCOME = 'The connection dropped before the till replied, so this may or may not have been done. Check before you try again.';
const OVERRIDE_MS = 1000;
const URL_GLOB = '**/api/buttons/recategorize';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5335', role: 'cashier' },
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
  await page.addInitScript((ms) => { (window as any).__utPromptTimeoutMs = ms; }, OVERRIDE_MS);
  await ensureOperator(page);
  await createCashier(page);
  await loginAsCashier(page);
  await page.goto('/');
}

type Late = { status: number; contentType: string; body: string };

// Every held route is released when its test ends, pass or fail (review).
const pendingReleases: Array<() => void> = [];
test.afterEach(() => { for (const r of pendingReleases.splice(0)) r(); });

// POST #1 continues (real prompt); POST #2 (the PIN submit) is held.
async function holdRetry(page: Page) {
  let posts = 0;
  let release!: (l?: Late | 'prompt') => void;
  const gate = new Promise<Late | 'prompt' | undefined>((r) => { release = r; });
  pendingReleases.push(() => release());
  let held!: () => void;
  const inFlight = new Promise<void>((r) => { held = r; });
  let promptHtml = '';
  await page.route(URL_GLOB, async (route: Route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    if (posts === 1) {
      const resp = await route.fetch();
      promptHtml = await resp.text();
      return route.fulfill({ response: resp, body: promptHtml });
    }
    held();
    const late = await gate;
    try {
      if (late === 'prompt') await route.fulfill({ status: 200, contentType: 'text/html', body: promptHtml });
      else if (late) await route.fulfill(late);
      else await route.abort();
    } catch { /* aborted */ }
  });
  return { posts: () => posts, release, inFlight };
}

async function submitPin(page: Page) {
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  await dlg.locator('input[name="override_pin"]').fill('123456');
  await dlg.locator('button[type=submit]').click();
  return dlg;
}

test.describe('PIN prompt timeout (ut-docs#5335)', () => {
  test('T9 htmx PIN submit held: dialog closes, outcome text, late answers ignored', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page);
    await page.evaluate(() => {
      const t = document.createElement('div');
      t.id = 'buttons-add-error';
      document.body.appendChild(t);
      (window as any).htmx.ajax('POST', '/api/buttons/recategorize', { target: '#buttons-add-error', swap: 'innerHTML', values: { item_id: 'x', category_id: '' } });
    });
    const dlg = await submitPin(page);
    await h.inFlight;
    await expect(dlg).toHaveJSProperty('open', false, { timeout: 5000 });
    await expect(page.locator('#pos-alert .notice-text')).toHaveText(OUTCOME);
    await expect(page.locator('#pos-alert')).toBeInViewport();
    expect(h.posts()).toBe(2);
    h.release({ status: 200, contentType: 'text/html', body: '<span id="late-5335">late</span>' });
    await page.waitForTimeout(500);
    await expect(page.locator('#late-5335')).toHaveCount(0);
    await expect(dlg).toHaveJSProperty('open', false);
    await expect(page.locator('#pos-alert .notice-text')).toHaveText(OUTCOME);
    expect(h.posts()).toBe(2);
  });

  async function startCall(page: Page) {
    await page.evaluate(() => {
      const w = window as any;
      w.__r = { done: null, cancelled: 0, net: null, rejected: false };
      w.utPostWithElevation('/api/buttons/recategorize', { item_id: 'x', category_id: '' },
        (r: Response) => { w.__r.done = r.status; },
        () => { w.__r.cancelled++; })
        .catch((e: any) => { w.__r.rejected = true; w.__r.net = !!e.utNetworkError; });
    });
  }
  const rec = (page: Page) => page.evaluate(() => (window as any).__r as { done: number | null; cancelled: number; net: boolean | null; rejected: boolean });

  test('T10 fetch PIN retry held: dialog closed, busy cleared, tagged, late re-prompt ignored', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page);
    await startCall(page);
    const dlg = await submitPin(page);
    await h.inFlight;
    await expect(dlg).toHaveJSProperty('open', false, { timeout: 5000 });
    await expect.poll(async () => (await rec(page)).rejected).toBe(true);
    expect(await rec(page)).toMatchObject({ net: true, done: null, cancelled: 0 });
    await expect(page.locator('#elevation-modal form.htmx-request')).toHaveCount(0);
    expect(h.posts()).toBe(2);
    h.release('prompt');
    await page.waitForTimeout(500);
    await expect(page.locator('#elevation-modal')).not.toHaveJSProperty('open', true);
    expect(await rec(page)).toMatchObject({ net: true, done: null, cancelled: 0 });
    expect(h.posts()).toBe(2);
  });

  test('T11 the retry clock starts at the PIN submit, not when the dialog opens', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page);
    await startCall(page);
    const dlg = page.locator('#elevation-modal');
    await expect(dlg).toHaveJSProperty('open', true);
    await page.waitForTimeout(OVERRIDE_MS * 2);
    await expect(dlg).toHaveJSProperty('open', true);
    expect((await rec(page)).rejected).toBe(false);
    await submitPin(page);
    await h.inFlight;
    await expect.poll(async () => (await rec(page)).rejected, { timeout: 5000 }).toBe(true);
    h.release();
  });
});
