import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1104: settings.html's exit-to-os form funneled EVERY non-2xx,
// non-503 status (including 429, the device-wide PIN lockout) into the same
// generic "Incorrect PIN or not authorized." message — an operator locked
// out by someone else's failed keypad attempts was told their PIN was
// wrong. The server side already returns 429 for a real lockout
// (internal/pages/settings_page_test.go's
// TestExitToOSEndpoint_LockedOutReturns429NotGeneric403); the bug lived
// entirely in the client JS's status-code branching, which only a real
// browser can exercise — a Go handler test can prove the status code, never
// that the page renders the right message for it. Intercepting the fetch is
// cheaper and more deterministic than actually burning the 5-attempt
// lockout budget through the UI.
test('exit-to-os 429 (locked out) renders the lockout message, not the generic PIN error', async ({ page }) => {
  // The mocked 429 below is a deliberate non-2xx response; Chromium logs its
  // own "Failed to load resource: ... 429" console error for it regardless
  // of how the page's own JS handles it (ut-docs#916's watchConsole note).
  const assertClean = watchConsole(page, /^Failed to load resource:.*429/);
  await page.goto('/settings');

  await page.route('**/api/settings/exit-to-os', async (route) => {
    await route.fulfill({ status: 429, contentType: 'text/plain', body: 'locked out' });
  });

  const msg = page.locator('#exit-to-os-msg');
  await page.locator('#exit-to-os-form [name="manager_pin"]').fill('000000');
  await page.locator('#exit-to-os-btn').click();

  const notice = msg.locator('.pos-notice.error');
  await expect(notice).toBeVisible();
  await expect(notice.locator('.notice-text')).toHaveText('Too many attempts — wait 30 seconds');
  await expect(notice.locator('.notice-text')).not.toHaveText('Incorrect PIN or not authorized.');

  await page.unroute('**/api/settings/exit-to-os');
  assertClean();
});

// The generic message must still be the one shown for an ordinary wrong-PIN
// 403 — proves the new 429 branch didn't swallow the pre-existing case it
// sits next to.
test('exit-to-os 403 (wrong PIN) still renders the generic PIN error, not the lockout message', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*403/);
  await page.goto('/settings');

  await page.route('**/api/settings/exit-to-os', async (route) => {
    await route.fulfill({ status: 403, contentType: 'text/plain', body: 'manager PIN required' });
  });

  const msg = page.locator('#exit-to-os-msg');
  await page.locator('#exit-to-os-form [name="manager_pin"]').fill('000000');
  await page.locator('#exit-to-os-btn').click();

  const notice = msg.locator('.pos-notice.error');
  await expect(notice).toBeVisible();
  await expect(notice.locator('.notice-text')).toHaveText('Incorrect PIN or not authorized.');
  await expect(notice.locator('.notice-text')).not.toHaveText('Too many attempts — wait 30 seconds');

  await page.unroute('**/api/settings/exit-to-os');
  assertClean();
});
