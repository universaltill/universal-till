import { test, expect } from './fixtures';
import { watchConsole, recordAlertAfterRequest } from './helpers';

// ut-docs#3978: `text-response:<id>` only trusts a body the server marked
// X-UT-Response: refused (httpx.RefuseText, already-translated operator
// text). An unmarked text body is an untranslated developer string: it must
// not be shown and must not hide the generic "server error" banner.
const REFUSED = /Failed to load resource:.*400|Response Status Error Code 400 from \/api\/settings\/display-mode/;
const PATH = '/api/settings/display-mode';

async function submitDisplayMode(page: any) {
  await page.goto('/settings?lang=en#settings-display');
  const form = page.locator('form[hx-post="/api/settings/display-mode"]');
  await Promise.all([
    page.waitForResponse((r: any) => r.url().includes(PATH) && r.status() === 400),
    form.getByRole('button').click(),
  ]);
}

test('settings: an unmarked 400 text body is not shown and keeps the generic banner', async ({ page }) => {
  const assertClean = watchConsole(page, REFUSED);
  await page.route('**' + PATH, (r) => r.fulfill({
    status: 400, contentType: 'text/plain; charset=utf-8',
    body: 'mode must be register, backoffice, or self_order',
  }));
  const alertHiddenAfter = await recordAlertAfterRequest(page, PATH);
  await submitDisplayMode(page);
  await expect(page.locator('#display-mode-msg')).toBeEmpty();
  expect(await alertHiddenAfter()).toBe(false);
  await expect(page.locator('#pos-alert')).toBeVisible();
  assertClean();
});

test('settings: a marked refused 400 text body is shown and replaces the generic banner', async ({ page }) => {
  const assertClean = watchConsole(page, REFUSED);
  await page.route('**' + PATH, (r) => r.fulfill({
    status: 400, contentType: 'text/plain; charset=utf-8', headers: { 'X-UT-Response': 'refused' },
    body: 'Choose a device profile.',
  }));
  const alertHiddenAfter = await recordAlertAfterRequest(page, PATH);
  await submitDisplayMode(page);
  await expect(page.locator('#display-mode-msg')).toHaveText('Choose a device profile.');
  expect(await alertHiddenAfter()).toBe(true);
  await expect(page.locator('#pos-alert')).toBeHidden();
  assertClean();
});

test('settings: an unmarked 400 clears a refusal shown by the previous submit', async ({ page }) => {
  const assertClean = watchConsole(page, REFUSED);
  let marked = true;
  await page.route('**' + PATH, (r) => r.fulfill(marked
    ? { status: 400, contentType: 'text/plain; charset=utf-8', headers: { 'X-UT-Response': 'refused' }, body: 'Choose a device profile.' }
    : { status: 400, contentType: 'text/plain; charset=utf-8', body: 'mode must be register, backoffice, or self_order' }));
  await submitDisplayMode(page);
  await expect(page.locator('#display-mode-msg')).toHaveText('Choose a device profile.');
  marked = false;
  const form = page.locator('form[hx-post="/api/settings/display-mode"]');
  await Promise.all([
    page.waitForResponse((r: any) => r.url().includes(PATH) && r.status() === 400),
    form.getByRole('button').click(),
  ]);
  await expect(page.locator('#display-mode-msg')).toBeEmpty();
  await expect(page.locator('#pos-alert')).toBeVisible();
  assertClean();
});
