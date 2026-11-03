import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#5304: on a 1024x600 till with the on-screen keyboard open on the
// manager-PIN dialog (#elevation-modal), the OSK (z 1000, fixed to the bottom)
// covered the dialog's Cancel/Confirm row. With body.osk-padded the dialog is
// now top-anchored, capped to the band above the OSK and scrolls inside
// itself, with the action row pinned to its bottom edge.
//
// Both ways the dialog appears are covered: utPostWithElevation's hand-built
// copy (carries hx-disable) and the htmx OOB swap (elevation_prompt.html).
// Chromium touch emulation only (hasTouch makes body[data-osk]=auto), NOT real
// Pi touch hardware. Needs the `auth` project.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier5304${RUN}`;
const CASHIER_PIN = '246813';
const URL_GLOB = '**/api/buttons/recategorize';

test.use({ hasTouch: true, viewport: { width: 1024, height: 600 } });

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 5304', role: 'cashier' },
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
  await ensureOperator(page);
  await createCashier(page);
  await loginAsCashier(page);
  await page.goto('/');
}

// Fetch path: utPostWithElevation inserts the dialog by hand.
async function openViaFetch(page: Page) {
  await page.evaluate(() => {
    (window as any).__c = 0;
    (window as any).utPostWithElevation('/api/buttons/recategorize', { item_id: 'x', category_id: '' },
      () => {}, () => { (window as any).__c++; });
  });
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  await expect(dlg).toHaveAttribute('hx-disable', '');
  return dlg;
}

// htmx path: a real htmx request whose 4xx text/html body carries the OOB prompt.
async function openViaHtmx(page: Page) {
  await page.evaluate(() => {
    (window as any).htmx.ajax('POST', '/api/buttons/recategorize', { target: 'body', swap: 'none', values: { item_id: 'x', category_id: '' } });
  });
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  await expect(dlg).not.toHaveAttribute('hx-disable', '');
  return dlg;
}

async function openOsk(page: Page) {
  await page.locator('#elevation-modal [data-osk-toggle]').click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('body')).toHaveClass(/osk-padded/);
}

// Cancel and Confirm lie wholly above the OSK, inside the viewport, and are
// what elementFromPoint finds at their centre; so does the dialog itself.
async function assertClear(page: Page) {
  await expect.poll(() => page.evaluate(() => {
    const dlg = document.getElementById('elevation-modal')!;
    const oskTop = document.getElementById('osk')!.getBoundingClientRect().top;
    const out: Record<string, unknown> = { oskTop };
    const d = dlg.getBoundingClientRect();
    out.dlgOk = d.top >= 0 && d.bottom <= oskTop;
    for (const [name, sel] of [['cancel', '[data-action="close-closest"]'], ['confirm', 'button[type=submit]']]) {
      const el = dlg.querySelector(sel) as HTMLElement;
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      out[name] = r.top >= 0 && r.bottom <= oskTop && !!hit && el.contains(hit);
    }
    return out;
  }), { message: 'dialog, Cancel and Confirm above the OSK and hit-testable' })
    .toMatchObject({ dlgOk: true, cancel: true, confirm: true });
}

test.describe('PIN dialog stays above the OSK at 1024x600 (ut-docs#5304)', () => {
  test('fetch path: Cancel/Confirm clear of the OSK, Cancel is tappable', async ({ page }) => {
    await setup(page);
    const dlg = await openViaFetch(page);
    await openOsk(page);
    await assertClear(page);
    await page.locator('#osk button[data-k="1"]').tap();
    await expect(dlg.locator('input[name="override_pin"]')).toHaveValue('1');
    await dlg.locator('[data-action="close-closest"]').tap(); // real tap, no force
    await expect(dlg).toHaveJSProperty('open', false);
    expect(await page.evaluate(() => (window as any).__c)).toBe(1);
  });

  test('htmx path: Cancel/Confirm clear of the OSK, Cancel closes', async ({ page }) => {
    await setup(page);
    const dlg = await openViaHtmx(page);
    await openOsk(page);
    await assertClear(page);
    await dlg.locator('[data-action="close-closest"]').tap();
    await expect(dlg).toHaveJSProperty('open', false);
  });

  test('wrong-PIN re-prompt (taller): still clear, scrolls inside, input reachable', async ({ page }) => {
    await setup(page);
    await openViaFetch(page);
    const dlg = page.locator('#elevation-modal');
    await openOsk(page);
    await dlg.locator('input[name="override_pin"]').fill('000000');
    await dlg.locator('button[type=submit]').tap();
    await expect(dlg.locator('.login-error')).toBeVisible();
    if (!(await page.locator('#osk').isVisible())) await openOsk(page);
    await assertClear(page);
    const m = await dlg.evaluate((el) => ({ sh: el.scrollHeight, ch: el.clientHeight, oy: getComputedStyle(el).overflowY }));
    expect(m.sh).toBeGreaterThan(m.ch); // scrolls inside itself, not under the OSK
    expect(m.oy).toBe('auto');
    await dlg.locator('input[name="override_pin"]').scrollIntoViewIfNeeded();
    const r = await dlg.locator('input[name="override_pin"]').boundingBox();
    const oskTop = (await page.locator('#osk').boundingBox())!.y;
    expect(r!.y).toBeGreaterThanOrEqual(0);
    expect(r!.y + r!.height).toBeLessThanOrEqual(oskTop);
  });

  test('closing the OSK restores the normal 8vh position', async ({ page }) => {
    await setup(page);
    const dlg = await openViaFetch(page);
    const before = await dlg.evaluate((el) => { const s = getComputedStyle(el); return { top: s.insetBlockStart, max: s.maxBlockSize, ov: s.overflowY }; });
    expect(before.top).toBe('48px');
    await openOsk(page);
    await dlg.locator('[data-osk-toggle]').click();
    await expect(page.locator('#osk')).toBeHidden();
    await expect(page.locator('body')).not.toHaveClass(/osk-padded/);
    const after = await dlg.evaluate((el) => { const s = getComputedStyle(el); return { top: s.insetBlockStart, max: s.maxBlockSize, ov: s.overflowY }; });
    expect(after).toEqual(before);
    await openOsk(page); // reopening works again
    await assertClear(page);
  });
});
