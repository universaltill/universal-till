import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3735: web/public/window-toggle.js. The shared e2e till has no
// attached desktop shell, so the server never renders <body data-window-toggle>
// here (that decision is covered by the Go tests, pages/window_toggle_api_test.go).
// This spec covers the client half in a real browser: the script is inert
// without the flag, and with the flag it POSTs on a plain F11 only.

const TOGGLE = '**/api/settings/window-mode/toggle';

// Dispatches a cancelable F11 keydown on the document and reports whether the
// page called preventDefault() on it (a real key press into a headless page
// would be indistinguishable from the browser default, so we observe it here).
async function pressF11(page: import('@playwright/test').Page, mods: { shiftKey?: boolean; ctrlKey?: boolean } = {}) {
  return page.evaluate((m) => {
    const ev = new KeyboardEvent('keydown', { key: 'F11', code: 'F11', bubbles: true, cancelable: true, ...m });
    document.dispatchEvent(ev);
    return ev.defaultPrevented;
  }, mods);
}

test('without the flag F11 is left to the browser and nothing is sent', async ({ page }) => {
  const assertClean = watchConsole(page);
  const posts: string[] = [];
  page.on('request', (r) => { if (r.url().includes('/window-mode/toggle')) posts.push(r.method()); });

  await page.goto('/settings#settings-display');
  await expect(page.locator('body')).not.toHaveAttribute('data-window-toggle', /.*/);
  expect(await pressF11(page)).toBe(false);
  await page.waitForTimeout(600);
  expect(posts).toEqual([]);
  assertClean();
});

test('with the flag a plain F11 POSTs once; modified F11 does not; kiosk-locked answer shows a non-modal chip', async ({ page }) => {
  const assertClean = watchConsole(page);
  const posts: { method: string; hx: string | undefined }[] = [];
  await page.route(TOGGLE, (route) => {
    posts.push({ method: route.request().method(), hx: route.request().headers()['hx-request'] });
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { window_mode: 'kiosk', applied: false, reason: 'kiosk_locked' }, error: null }),
    });
  });

  await page.goto('/settings#settings-display');
  await page.evaluate(() => {
    document.body.setAttribute('data-window-toggle', '1');
    document.body.setAttribute('data-f11-kiosk-locked', 'Kiosk mode is locked. Use Exit to OS window in Settings.');
  });

  // Modified F11 is never claimed.
  expect(await pressF11(page, { shiftKey: true })).toBe(false);
  expect(await pressF11(page, { ctrlKey: true })).toBe(false);
  await page.waitForTimeout(600);
  expect(posts).toEqual([]);

  // Plain F11 is claimed and POSTed exactly once (second press inside the debounce is swallowed).
  expect(await pressF11(page)).toBe(true);
  expect(await pressF11(page)).toBe(true);
  const chip = page.locator('#pos-alert');
  await expect(chip).toBeVisible();
  await expect(chip.locator('.notice-text')).toHaveText(/Kiosk mode is locked/);
  expect(posts).toEqual([{ method: 'POST', hx: 'true' }]);

  // Non-modal: no open <dialog>, the status bar / rest of the page stays interactive.
  await expect(page.locator('dialog[open]')).toHaveCount(0);
  const modalOpen = await page.evaluate(() => !!document.querySelector('dialog:modal'));
  expect(modalOpen).toBe(false);
  assertClean();
});

test('applied answer shows no chip', async ({ page }) => {
  await page.route(TOGGLE, (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ data: { window_mode: 'fullscreen', applied: true, reason: '' }, error: null }),
  }));
  await page.goto('/settings#settings-display');
  await page.evaluate(() => document.body.setAttribute('data-window-toggle', '1'));
  const req = page.waitForRequest(TOGGLE);
  expect(await pressF11(page)).toBe(true);
  await req;
  await page.waitForTimeout(300);
  await expect(page.locator('#pos-alert')).toBeHidden();
});

for (const [name, size] of [['1024x600', { width: 1024, height: 600 }], ['360', { width: 360, height: 640 }]] as const) {
  test(`kiosk-locked chip fits at ${name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(size);
    await page.route(TOGGLE, (route) => route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ data: { window_mode: 'kiosk', applied: false, reason: 'kiosk_locked' }, error: null }),
    }));
    await page.goto('/');
    await page.evaluate(() => {
      document.body.setAttribute('data-window-toggle', '1');
      document.body.setAttribute('data-f11-kiosk-locked', 'Kiosk mode is locked. Use Exit to OS window in Settings.');
    });
    await pressF11(page);
    const chip = page.locator('#pos-alert');
    await expect(chip).toBeVisible();
    const box = await chip.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(size.width + 0.5);
    expect(box!.y).toBeGreaterThanOrEqual(0);
    expect(box!.y + box!.height).toBeLessThanOrEqual(size.height);
    // The dismiss control stays inside the chip and reachable.
    const dismiss = await chip.locator('.notice-dismiss').boundingBox();
    expect(dismiss!.x + dismiss!.width).toBeLessThanOrEqual(size.width + 0.5);
    await page.screenshot({ path: `/tmp/claude-0/s/3735/chip-${name}.png` });
    expect(testInfo.status).toBeDefined();
  });
}

// KNOWN DEFECT (Tester finding, ut-docs#3735): window-toggle.js showChip() does
// not scroll #pos-alert (an in-flow sibling at the top of the document) into
// view, unlike app.js's outcome/timeout path (ut-docs#5298). On a scrolled
// Settings page the "Kiosk mode is locked" chip lands above the viewport.
test('kiosk-locked chip is visible on a scrolled page', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.route(TOGGLE, (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ data: { window_mode: 'kiosk', applied: false, reason: 'kiosk_locked' }, error: null }),
  }));
  await page.goto('/settings#settings-display');
  await page.evaluate(() => {
    document.body.setAttribute('data-window-toggle', '1');
    document.body.setAttribute('data-f11-kiosk-locked', 'Kiosk mode is locked.');
    window.scrollTo(0, 400);
  });
  await pressF11(page);
  const chip = page.locator('#pos-alert');
  await expect(chip).toBeVisible();
  await expect(chip).toBeInViewport();
});
