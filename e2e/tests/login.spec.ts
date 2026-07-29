import { test, expect, Page } from '@playwright/test';
import { watchConsole } from './helpers';

// Drives the AUTH project's server (playwright.config.ts) — a genuinely
// fresh install with auth ON, separate from every other spec's
// already-logged-in-by-default till. Covers the real day-one flow: a
// brand-new till redirects into the guided setup wizard, the wizard
// creates the admin PIN, PIN login works after that, a protected page
// is unreachable without a session, and logging out actually locks it
// back down.
//
// One shared page across the whole file (test.describe.serial + a single
// browser context): the session cookie set by logging in must carry
// forward from one test to the next, same as a real operator's browser
// tab. Playwright gives every `test()` a fresh, cookie-less context by
// default, which would make each step look logged-out again.
test.describe.serial('first-boot setup and PIN login', () => {
  let page: Page;
  let assertClean: () => void;

  test.beforeAll(async ({ browser }) => {
    page = await browser.newPage();
    assertClean = watchConsole(page);
  });
  test.afterAll(async () => {
    await page.close();
  });

  test('a brand-new till redirects straight into the setup wizard', async () => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/setup/);
    await expect(page.locator('body')).toContainText('Choose your language');
  });

  test('completing the wizard creates the admin PIN and logs in', async () => {
    // Step 1 · language — just advance.
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 2 · country (prefills currency/tax client-side).
    await page.locator('select[name=country]').selectOption('GB');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 3 · shop name.
    await page.locator('input[name=store_name]').fill('E2E Test Shop');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 4 · admin PIN.
    await page.locator('input[name=pin]').fill('482913');
    await page.locator('input[name=pin_confirm]').fill('482913');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 5 · finish — real form submit, real redirect to the till.
    await Promise.all([
      page.waitForURL((u) => !u.pathname.includes('/setup')),
      page.locator('button[type=submit]', { hasText: 'Start selling' }).click(),
    ]);
    await expect(page.locator('#basket')).toBeVisible();
  });

  test('a protected page is unreachable while locked, then the PIN logs back in', async () => {
    await expect(page.locator('#basket')).toBeVisible();

    // Lock the till.
    await page.locator('.session-lock button').click();
    await expect(page).toHaveURL(/\/login/);
    await expect(page.locator('.pin-pad')).toBeVisible();

    // A protected page must bounce back to /login while locked.
    await page.goto('/inventory');
    await expect(page).toHaveURL(/\/login/);

    // Log back in with the PIN set during the wizard.
    for (const d of ['4', '8', '2', '9', '1', '3']) {
      await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
    }
    await page.locator('button[type=submit].pin-key').click();
    await expect(page).toHaveURL('/');
    await expect(page.locator('#basket')).toBeVisible();
  });

  test('a wrong PIN is rejected', async () => {
    // Lock again so the PIN pad is reachable (GET /login redirects
    // straight back to / while a session cookie is still valid).
    await page.locator('.session-lock button').click();
    await expect(page).toHaveURL(/\/login/);

    for (const d of ['0', '0', '0', '0']) {
      await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
    }
    await page.locator('button[type=submit].pin-key').click();
    await expect(page.locator('.login-error')).toBeVisible();
    assertClean();
  });
});
