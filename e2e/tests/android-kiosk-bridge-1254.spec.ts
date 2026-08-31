import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1254 (review should-fix 4): the Android native shell's kiosk
// lock-task only ever releases via login.html/settings.html calling
// window.AndroidKiosk.exitLockdown() — added by MainActivity.kt's
// addJavascriptInterface, invisible to a Go handler test, which can prove
// the endpoint's status code but never that the page's JS actually calls
// the native bridge for it. This is the exact situation
// exit-to-os-lockout-1104.spec.ts already exists to cover ("the bug lived
// entirely in the client JS's status-code branching, which only a real
// browser can exercise") — same technique: install a recording stub for
// window.AndroidKiosk before the page's own script runs, mock the
// endpoint's response, assert the bridge is called on a real success and
// NOT called on any error status. This is also the regression test for the
// bug this ticket's own design was rewritten to avoid: an earlier draft
// unlocked on the WebView simply landing on /settings, which would have
// made this exact assertion pass for the wrong reason — these tests key
// off the same success/failure axis a real Android build depends on.
//
// window.AndroidKiosk does not exist on every other platform (it's only
// ever injected by the Android native shell) — these tests simulate that
// shell via addInitScript, so a plain desktop/Pi WebView run of this same
// page (any other e2e test in this suite) is unaffected; the guard in both
// pages' own JS (`if (window.AndroidKiosk && window.AndroidKiosk.exitLockdown)`)
// is exactly what makes that a safe no-op there.
function installKioskStub() {
  return `
    window.__androidKioskCalls = [];
    window.AndroidKiosk = {
      exitLockdown: function () { window.__androidKioskCalls.push('exitLockdown'); }
    };
  `;
}

async function callCount(page: import('@playwright/test').Page): Promise<number> {
  return page.evaluate(() => (window as any).__androidKioskCalls?.length ?? 0);
}

test.describe('settings.html exit-to-os → window.AndroidKiosk bridge', () => {
  test('204 (real exit) calls AndroidKiosk.exitLockdown exactly once', async ({ page }) => {
    await page.addInitScript(installKioskStub());
    await page.goto('/settings');

    await page.route('**/api/settings/exit-to-os', async (route) => {
      await route.fulfill({ status: 204 });
    });

    await page.locator('#exit-to-os-form [name="manager_pin"]').fill('482913');
    await page.locator('#exit-to-os-btn').click();

    await expect(page.locator('#exit-to-os-msg .pos-notice.success')).toBeVisible();
    expect(await callCount(page)).toBe(1);

    await page.unroute('**/api/settings/exit-to-os');
  });

  test('503 (no window control) does NOT call AndroidKiosk.exitLockdown', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*503/);
    await page.addInitScript(installKioskStub());
    await page.goto('/settings');

    await page.route('**/api/settings/exit-to-os', async (route) => {
      await route.fulfill({ status: 503, contentType: 'text/plain', body: 'window control unavailable: no_shell' });
    });

    await page.locator('#exit-to-os-form [name="manager_pin"]').fill('482913');
    await page.locator('#exit-to-os-btn').click();

    await expect(page.locator('#exit-to-os-msg .pos-notice.error')).toBeVisible();
    expect(await callCount(page)).toBe(0);

    await page.unroute('**/api/settings/exit-to-os');
    assertClean();
  });

  test('403 (wrong PIN) does NOT call AndroidKiosk.exitLockdown', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*403/);
    await page.addInitScript(installKioskStub());
    await page.goto('/settings');

    await page.route('**/api/settings/exit-to-os', async (route) => {
      await route.fulfill({ status: 403, contentType: 'text/plain', body: 'manager PIN required' });
    });

    await page.locator('#exit-to-os-form [name="manager_pin"]').fill('000000');
    await page.locator('#exit-to-os-btn').click();

    await expect(page.locator('#exit-to-os-msg .pos-notice.error')).toBeVisible();
    expect(await callCount(page)).toBe(0);

    await page.unroute('**/api/settings/exit-to-os');
    assertClean();
  });
});

// The /login-screen half of this coverage (login.html's own exit-to-os
// form, reachable session-less) lives in login.spec.ts instead of here —
// see the note there. It needs a till that has already completed first
// boot (a real admin PIN exists), which only the AUTH project's
// describe.serial wizard flow provides in this repo's e2e setup; this
// file's settings.html tests above run fine against the plain auth-off
// default till, so they stay here.
