import { test, expect } from './fixtures';
import { watchConsole, recordAlertAfterRequest } from './helpers';

// ut-docs#3247: a Settings card that writes the server's own translated
// refusal into its message span (`text-response:<id>`) must not also leave
// the page-wide "server error" banner (#pos-alert, raised by app.js's
// htmx:responseError for every non-2xx) up on top of it.
const REFUSED = /Failed to load resource:.*400|Response Status Error Code 400 from \/api\/settings\/staff-languages/;

test('settings: a staff-languages refusal replaces the generic server-error banner', async ({ page }) => {
  const assertClean = watchConsole(page, REFUSED);
  const alertHiddenAfter = await recordAlertAfterRequest(page, '/api/settings/staff-languages');
  await page.goto('/settings?lang=en#settings-staff-languages');
  const card = page.locator('#settings-staff-languages');
  // The shop default is always posted via a hidden input; dropping it is
  // the only way to make the server refuse the list (real refusal, no stub).
  await card.locator('form').evaluate((f) => {
    f.querySelectorAll('input[type="hidden"][name="staff_locales"]').forEach((i) => i.remove());
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/staff-languages') && r.status() === 400),
    card.getByRole('button').click(),
  ]);
  await expect(page.locator('#staff-languages-msg')).not.toBeEmpty();
  // Recorded at request end: the banner self-heals on the next 2xx, so a
  // live toBeHidden() alone could pass late on the bug.
  expect(await alertHiddenAfter()).toBe(true);
  await expect(page.locator('#pos-alert')).toBeHidden();
  assertClean();
});

test('settings: an empty 400 body leaves the generic server-error banner up', async ({ page }) => {
  const assertClean = watchConsole(page, REFUSED);
  await page.route('**/api/settings/staff-languages', (r) => r.fulfill({
    status: 400, contentType: 'text/plain; charset=utf-8', headers: { 'X-UT-Response': 'refused' }, body: '',
  }));
  const alertHiddenAfter = await recordAlertAfterRequest(page, '/api/settings/staff-languages');
  await page.goto('/settings?lang=en#settings-staff-languages');
  const card = page.locator('#settings-staff-languages');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/staff-languages') && r.status() === 400),
    card.getByRole('button').click(),
  ]);
  await expect(page.locator('#staff-languages-msg')).toBeEmpty();
  expect(await alertHiddenAfter()).toBe(false);
  assertClean();
});
