import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5291 (follow-up to #5288): window.utPostWithElevation inserts the
// manager-PIN dialog by hand and drives its retry with fetch, so htmx never
// marks the form in flight. The retry is not idempotent: while it runs, the
// form carries .htmx-request + aria-busy, its buttons are disabled, Cancel and
// Escape are inert, exactly one of onDone/onCancel fires, and a closed dialog
// is never re-opened by a late re-prompt.
//
// The shared helper is driven directly with recording callbacks: the real
// callers sit on manager-only pages a cashier cannot open, and a manager is
// never prompted. /api/buttons/recategorize is gated by catalog_management, so
// a cashier gets the real text/html prompt. The held retry is answered by the
// test, so nothing changes on the till.
//
// Needs the `auth` project. Sorts after login.spec.ts.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5291${RUN}`;
const CASHIER_PIN = '246813';
const URL_GLOB = '**/api/buttons/recategorize';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5291', role: 'cashier' },
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

type Mode = 'ok' | 'reprompt' | 'abort';

// Routes POST #1 through (raising the real prompt) and holds POST #2.
async function holdRetry(page: Page, mode: Mode) {
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  let held!: () => void;
  const inFlight = new Promise<void>((r) => { held = r; });
  let posts = 0;
  await page.route(URL_GLOB, async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    if (posts === 1) return route.continue();
    held();
    await gate;
    if (mode === 'abort') return route.abort();
    if (mode === 'reprompt') return route.continue(); // wrong PIN: the real re-prompt
    return route.fulfill({ status: 200, contentType: 'text/plain', body: 'ok-5291' });
  });
  return { release, inFlight, posts: () => posts };
}

async function startCall(page: Page) {
  await page.evaluate(() => {
    (window as any).__r = { done: null, cancelled: 0 };
    (window as any).utPostWithElevation('/api/buttons/recategorize', { item_id: 'x', category_id: '' },
      (r: Response) => { r.text().then((t) => { (window as any).__r.done = r.status + ':' + t; }); },
      () => { (window as any).__r.cancelled++; });
  });
}

const rec = (page: Page) => page.evaluate(() => (window as any).__r as { done: string | null; cancelled: number });

async function setup(page: Page) {
  await ensureOperator(page);
  await createCashier(page);
  await loginAsCashier(page);
  await page.goto('/');
}

async function submitPin(page: Page) {
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  await dlg.locator('input[name="override_pin"]').fill('123456');
  await dlg.locator('button[type=submit]').click();
  return dlg;
}

test.describe('utPostWithElevation retry in flight (ut-docs#5291)', () => {
  test('busy state, inert Cancel/Escape, then the answer goes to onDone', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page, 'ok');
    await startCall(page);
    const dlg = await submitPin(page);
    await h.inFlight;

    const form = dlg.locator('form');
    const submit = dlg.locator('button[type=submit]');
    const cancel = dlg.locator('[data-action="close-closest"]');
    await expect(cancel).toBeDisabled();
    await expect(submit).toBeDisabled();
    await expect(dlg.locator('[data-osk-toggle]')).toBeEnabled();
    await expect(form).toHaveClass(/htmx-request/);
    await expect(form).toHaveAttribute('aria-busy', 'true');
    const spinner = await submit.evaluate((el) => getComputedStyle(el, '::after').content);
    expect(spinner).not.toBe('none');

    await page.keyboard.press('Escape');
    await cancel.click({ force: true });
    await expect(dlg).toHaveJSProperty('open', true);
    expect((await rec(page)).cancelled).toBe(0);

    h.release();
    await expect(dlg).toHaveJSProperty('open', false);
    await expect.poll(async () => (await rec(page)).done).toBe('200:ok-5291');
    expect((await rec(page)).cancelled).toBe(0);
  });

  test('a wrong-PIN re-prompt is shown with enabled buttons; Cancel then reports onCancel', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page, 'reprompt');
    await startCall(page);
    await submitPin(page);
    await h.inFlight;
    h.release();

    const dlg = page.locator('#elevation-modal');
    await expect(dlg.locator('.login-error')).toBeVisible();
    await expect(dlg).toHaveJSProperty('open', true);
    await expect(dlg.locator('button[type=submit]')).toBeEnabled();
    await expect(dlg.locator('[data-action="close-closest"]')).toBeEnabled();
    await expect(dlg.locator('form')).not.toHaveClass(/htmx-request/);
    await expect(dlg.locator('form')).not.toHaveAttribute('aria-busy', 'true');
    let r = await rec(page);
    expect(r.cancelled).toBe(0);
    expect(r.done).toBeNull();

    await dlg.locator('[data-action="close-closest"]').click();
    await expect(dlg).toHaveJSProperty('open', false);
    await expect.poll(async () => (await rec(page)).cancelled).toBe(1);
  });

  test('a dialog closed out of band is not re-opened by a late re-prompt', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page, 'reprompt');
    await startCall(page);
    await submitPin(page);
    await h.inFlight;
    await page.evaluate(() => (document.getElementById('elevation-modal') as HTMLDialogElement).close());
    const answered = page.waitForResponse((r) => r.url().includes('/api/buttons/recategorize') && r.request().method() === 'POST' && r.request().postData()?.includes('override_pin') === true);
    h.release();
    await answered;
    // Let the page process the re-prompt before asserting it did not re-open.
    await page.evaluate(() => new Promise((r) => setTimeout(r, 300)));
    await expect.poll(async () => (await rec(page)).cancelled).toBe(1);
    await expect(page.locator('#elevation-modal')).toHaveJSProperty('open', false);
    expect((await rec(page)).done).toBeNull();
  });

  test('a network error clears the busy state, closes the dialog and calls onCancel', async ({ page }) => {
    await setup(page);
    const h = await holdRetry(page, 'abort');
    await startCall(page);
    const dlg = await submitPin(page);
    await h.inFlight;
    h.release();
    await expect(dlg).toHaveJSProperty('open', false);
    await expect.poll(async () => (await rec(page)).cancelled).toBe(1);
    expect((await rec(page)).done).toBeNull();
    await expect(page.locator('#elevation-modal form.htmx-request')).toHaveCount(0);
  });
});
