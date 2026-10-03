import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3308: renaming this till to a name another till in the shop
// already uses comes back 422 with the translated reason, which shows next
// to the Save button — not the page-wide "could not save" banner — and the
// field keeps what was typed. The refusal itself is the server's
// (TestTillNameEndpoint_*RefusesA*Name, Go); the e2e shop has no other
// till to collide with, so the 422 is stubbed here to prove the wiring.
test('settings: a till name already in use is refused inline', async ({ page }) => {
  const assertClean = watchConsole(page, /Failed to load resource:.*422|Response Status Error Code 422 from \/api\/settings\/till-name/);
  const reason = "That till name is already in use on this shop's network.";
  await page.route('**/api/settings/till-name', (r) => r.fulfill({
    status: 422, contentType: 'text/plain; charset=utf-8', body: reason + '\n',
  }));
  await page.goto('/settings?lang=en#settings-tills');
  const tn = page.locator('#settings-till-name-region');
  await tn.locator('input[name="name"]').fill('Terrace 3308');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/till-name') && r.status() === 422),
    tn.getByRole('button').click(),
  ]);
  await expect(page.locator('#till-name-msg')).toContainText(reason);
  await expect(page.locator('#settings-save-error')).toBeHidden();
  await expect(tn.locator('input[name="name"]')).toHaveValue('Terrace 3308');
  assertClean();
});
