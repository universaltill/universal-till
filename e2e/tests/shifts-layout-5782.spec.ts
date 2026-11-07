import { test, expect } from './fixtures';
import type { APIRequestContext, Page } from '@playwright/test';

// ut-docs#5782: /shifts at 1024x600 (kiosk Pi) pushed the close form and
// #shift-result below the 20-row History, so the operator had to scroll to
// close a shift; at 360px the History table widened the page after an
// open/close swap (UT.refreshRegion); and the Current Shift card printed
// the raw cashier id. The layout is now two columns from 900px, History
// scrolls inside its card, and the card shows the cashier's name.

const NAME = 'Layout Tester 5782';
const USERNAME = `lay5782_${Date.now()}`;

const attr = (html: string, re: RegExp) => (html.match(re) || [])[1] || '';

// Opens and closes shifts through the API (cashier_id explicit: the default
// project runs auth off, so there is no session user to default to).
async function closeAnyOpen(request: APIRequestContext) {
  const html = await (await request.get('/shifts')).text();
  const id = attr(html, /<form id="close-shift-form"[\s\S]*?name="shift_id" value="([^"]+)"/);
  if (id) {
    const r = await request.post('/api/shifts/close', { form: { shift_id: id, closing_cash: '0' } });
    expect(r.status(), 'close leftover shift').toBe(200);
  }
}

let cashierId = '';
let registerId = '';

async function openShift(request: APIRequestContext) {
  const r = await request.post('/api/shifts/open', {
    form: { register_id: registerId, cashier_id: cashierId, opening_cash: '1000' },
  });
  expect(r.status(), 'open shift').toBe(200);
}

test.beforeAll(async ({ request }) => {
  await closeAnyOpen(request);
  const created = await request.post('/api/users', {
    form: { username: USERNAME, display_name: NAME, role: 'cashier' },
  });
  expect(created.status(), 'create user').toBeLessThan(400);
  const users = await (await request.get('/users')).text();
  const row = users.split('<tr>').find((c) => c.includes(USERNAME)) || '';
  cashierId = attr(row, /hx-post="\/api\/users\/([^"/]+)\/active"/);
  expect(cashierId, 'cashier id parsed from /users').not.toBe('');
  registerId = attr(await (await request.get('/shifts')).text(), /name="register_id"[\s\S]*?<option value="([^"]+)"/);
  expect(registerId, 'register id parsed from /shifts').not.toBe('');
  // 20 closed shifts so History is full (the page lists the latest 20).
  for (let i = 0; i < 20; i++) {
    await openShift(request);
    await closeAnyOpen(request);
  }
});

test.afterAll(async ({ request }) => {
  await closeAnyOpen(request);
});

const hScroll = (page: Page) =>
  page.evaluate(() => {
    const e = document.scrollingElement!;
    return { sw: e.scrollWidth, cw: e.clientWidth };
  });

// Emulates the manager-PIN rows the template adds for a session without
// cash_adjustment (the default project is auth-off, so canPerform is always
// true and the server never renders them): same classes and order as
// web/ui/pages/shifts.html, to measure the worst-case close form height.
async function injectPinRows(page: Page) {
  await page.evaluate(() => {
    const grid = document.querySelector('#close-shift-form .shifts-close-grid')!;
    const note = grid.querySelector('.shifts-close-note')!;
    note.classList.remove('shifts-close-wide');
    const pin = document.createElement('label');
    pin.className = 'shifts-close-pin';
    pin.innerHTML = 'Manager PIN <input type="password" name="manager_pin_emulated" inputmode="numeric">';
    const hint = document.createElement('p');
    hint.className = 'muted shifts-close-hint';
    hint.style.fontSize = '.82rem';
    hint.textContent = 'A skim above the limit needs a manager PIN or a manager signed in.';
    note.before(pin, hint);
  });
}

// The visible area ends where the fixed status bar starts (it covers the
// bottom ~33px of a 600px viewport), so that is the limit, not the viewport.
const statusTop = (page: Page) => page.locator('.statusbar').evaluate((el) => el.getBoundingClientRect().top);

async function expectCloseControlsInViewport(page: Page, vh: number) {
  vh = (await statusTop(page)) || vh;
  expect(await page.evaluate(() => window.scrollY), 'page did not scroll').toBe(0);
  const submit = await page.locator('#close-shift-form button[type=submit]').boundingBox();
  const result = await page.locator('#shift-result').boundingBox();
  expect(submit, 'submit box').not.toBeNull();
  expect(result, 'result box').not.toBeNull();
  test.info().annotations.push({ type: 'measured', description: `submit bottom ${Math.round(submit!.y + submit!.height)}, result bottom ${Math.round(result!.y + result!.height)} of ${vh}` });
  expect(submit!.y + submit!.height, 'close submit bottom').toBeLessThanOrEqual(vh);
  expect(result!.y + result!.height, '#shift-result bottom').toBeLessThanOrEqual(vh);
}

test.describe('kiosk 1024x600', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('open shift: cashier name shown, close submit and #shift-result fit without scrolling', async ({ page, request }) => {
    await closeAnyOpen(request);
    await openShift(request);
    await page.goto('/shifts');
    const cell = page.locator('#shifts-page .card', { hasText: 'Current Shift' }).locator('tr', { hasText: 'Cashier' }).first().locator('td').nth(1);
    await expect(cell).toHaveText(NAME);
    await expect(cell).not.toHaveText(cashierId);
    await expect(page.locator('.shifts-history-scroll tbody tr')).toHaveCount(20); // the page lists the latest 20 (the open one included)
    await expectCloseControlsInViewport(page, 600);
    await page.screenshot({ path: test.info().outputPath('shifts-1024x600.png') });
  });

  test('worst case with the manager-PIN rows still fits', async ({ page, request }) => {
    await closeAnyOpen(request);
    await openShift(request);
    await page.goto('/shifts');
    await injectPinRows(page);
    await expectCloseControlsInViewport(page, 600);
    await page.screenshot({ path: test.info().outputPath('shifts-1024x600-pin.png') });
  });

  test('after closing, the confirmation is visible without scrolling', async ({ page, request }) => {
    await closeAnyOpen(request);
    await openShift(request);
    await page.goto('/shifts');
    await page.locator('#closing-cash').fill('10.00');
    await page.locator('#close-shift-form button[type=submit]').click();
    await expect(page.locator('#open-shift-form')).toBeVisible();
    await expect(page.locator('#shift-result')).not.toBeEmpty();
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
    const result = await page.locator('#shift-result').boundingBox();
    expect(result!.y + result!.height).toBeLessThanOrEqual(await statusTop(page));
    const submit = await page.locator('#open-shift-form button[type=submit]').boundingBox();
    expect(submit!.y + submit!.height).toBeLessThanOrEqual(await statusTop(page));
  });
});

test.describe('phone 360px', () => {
  test.use({ viewport: { width: 360, height: 640 }, hasTouch: true, isMobile: true });

  test('no horizontal page scroll on load, after open, after close', async ({ page, request }) => {
    await closeAnyOpen(request);
    await page.goto('/shifts');
    await page.waitForLoadState('load');
    let m = await hScroll(page);
    expect(m.sw, 'load (no shift open)').toBeLessThanOrEqual(m.cw);

    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
    m = await hScroll(page);
    expect(m.sw, 'after open (refreshRegion)').toBeLessThanOrEqual(m.cw);

    await page.locator('#closing-cash').fill('10.00');
    await page.locator('#close-shift-form button[type=submit]').click();
    await expect(page.locator('#open-shift-form')).toBeVisible();
    m = await hScroll(page);
    expect(m.sw, 'after close (refreshRegion)').toBeLessThanOrEqual(m.cw);
    await page.screenshot({ path: test.info().outputPath('shifts-360.png'), fullPage: true });
  });
});
