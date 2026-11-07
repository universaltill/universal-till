import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ensureOperator, markTourDone, openPhoneSheet } from './helpers';

// ut-docs#3560 slice A: the sale screen's No sale (open cash drawer) button.
// Auth project: a real manager session (the first-boot admin) and a cashier
// without the no_sale permission, who gets the shared manager-PIN dialog.
// The "printer" is a plain temp file (printer.mode=device), so the bytes the
// till would send to a thermal printer can be read back by size. A real drawer
// kick on real ESC/POS hardware is not covered here.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier3560${RUN}`;
const CASHIER_PIN = '135792';
const MANAGER_PIN = '482913'; // ADMIN_PIN of ensureOperator

let dev = '';
const size = () => statSync(dev).size;

async function setPrinter(page: Page, mode: string) {
  const r = await page.request.post('/api/settings/printer', {
    form: { mode, device: dev, address: '', charset: 'utf8', receiptPolicy: 'always', drawerPin: '2' },
  });
  expect(r.ok(), 'set printer').toBe(true);
}

async function createCashier(page: Page) {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 3560', role: 'cashier' },
  });
  expect(createResp.ok() || createResp.status() === 409, 'create cashier').toBe(true);
  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];
  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginWith(page: Page, pin: string) {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of pin.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
  await markTourDone(page);
}

test.beforeEach(async ({ page }) => {
  dev = join(mkdtempSync(join(tmpdir(), 'ut-nosale-')), 'lp0');
  writeFileSync(dev, '');
  await ensureOperator(page);
  await setPrinter(page, 'device');
  await page.goto('/');
});

test.afterEach(async ({ page }) => {
  await page.request.post('/api/auth/logout');
  await loginWith(page, MANAGER_PIN);
  await setPrinter(page, 'off');
});

test('manager: one tap opens the drawer, inline notice, basket untouched', async ({ page }) => {
  const basketBefore = await page.locator('#basket').innerHTML();
  const btn = page.getByTestId('no-sale-open');
  await expect(btn).toBeVisible();
  await btn.click();
  await expect(page.locator('#no-sale-status')).toContainText('Cash drawer opened.');
  expect(size()).toBeGreaterThan(0);
  expect(await page.locator('#basket').innerHTML()).toBe(basketBefore);
});

test('double click sends exactly one kick', async ({ page }) => {
  await page.getByTestId('no-sale-open').dblclick();
  await expect(page.locator('#no-sale-status')).toContainText('Cash drawer opened.');
  const one = size();
  // A single tap on a fresh file sends the same number of bytes.
  writeFileSync(dev, '');
  await page.getByTestId('no-sale-open').click();
  await expect.poll(size).toBe(one);
});

test('button is disabled while the request is in flight', async ({ page }) => {
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  await page.route('**/api/pos/no-sale', async (route) => { await gate; await route.continue(); });
  const btn = page.getByTestId('no-sale-open');
  await btn.click();
  await expect(btn).toBeDisabled();
  release();
  await expect(page.locator('#no-sale-status')).toContainText('Cash drawer opened.');
  await expect(btn).toBeEnabled();
});

test('drawer kick failure shows an error notice and nothing is sent', async ({ page }) => {
  const r = await page.request.post('/api/settings/printer', {
    form: { mode: 'device', device: join(dev, 'gone', 'lp0'), charset: 'utf8', receiptPolicy: 'always', drawerPin: '2' },
  });
  expect(r.ok()).toBe(true);
  await page.goto('/');
  await page.getByTestId('no-sale-open').click();
  await expect(page.locator('#no-sale-status')).toContainText('The cash drawer could not be opened.');
  expect(size()).toBe(0);
});

test('hidden when printing is off', async ({ page }) => {
  await setPrinter(page, 'off');
  await page.goto('/');
  await expect(page.getByTestId('no-sale-open')).toHaveCount(0);
});

test('cashier: manager PIN dialog (not modal), wrong PIN, then approval', async ({ page }) => {
  await createCashier(page);
  await loginWith(page, CASHIER_PIN);
  await page.goto('/');
  await page.getByTestId('no-sale-open').click();
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toBeVisible();
  expect(await dlg.evaluate((el) => el.matches(':modal'))).toBe(false);
  expect(size()).toBe(0);

  await dlg.locator('input[name=override_pin]').fill('000000');
  await dlg.locator('button[type=submit]').click();
  await expect(dlg.locator('.login-error')).toHaveText('Invalid PIN');
  expect(size()).toBe(0);

  await dlg.locator('input[name=override_pin]').fill(MANAGER_PIN);
  await dlg.locator('button[type=submit]').click();
  await expect(page.locator('#no-sale-status')).toContainText('Cash drawer opened.');
  expect(size()).toBeGreaterThan(0);
});

test('cashier: cancelling the PIN dialog records nothing', async ({ page }) => {
  await loginWith(page, CASHIER_PIN);
  await page.goto('/');
  await page.getByTestId('no-sale-open').click();
  const dlg = page.locator('#elevation-modal');
  await expect(dlg).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dlg).toBeHidden();
  expect(size()).toBe(0);
});

test('360x640: the icon is reachable in the basket sheet, no horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 640 });
  await page.goto('/');
  await openPhoneSheet(page);
  const btn = page.getByTestId('no-sale-open');
  await expect(btn).toBeVisible();
  const g = await btn.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      inside: r.left >= 0 && r.right <= window.innerWidth && r.top >= 0 && r.bottom <= window.innerHeight,
      hit: !!hit && (hit === el || el.contains(hit)),
      noHScroll: document.documentElement.scrollWidth <= document.documentElement.clientWidth,
    };
  });
  expect(g).toEqual({ inside: true, hit: true, noHScroll: true });
});

test('1024x600 till: the footer row stays one line with all controls', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/');
  const footer = page.locator('.tender-default-footer');
  const h = await footer.evaluate((el) => el.getBoundingClientRect().height / parseFloat(getComputedStyle(document.documentElement).fontSize));
  expect(h).toBeLessThanOrEqual(2.8);
  await expect(page.getByTestId('no-sale-open')).toBeVisible();
});
