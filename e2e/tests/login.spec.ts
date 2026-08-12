import { test, expect, Page } from '@playwright/test';
import { watchConsole, fieldGeometry, expectStacked } from './helpers';

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

  // ut-docs#344. Two defects, one screen, and only a real browser catches
  // either: the page never loaded htmx.min.js, so the hx-post was inert
  // markup and the Join button did nothing at all; and htmx 1.9 DISCARDS the
  // response body on a non-2xx status, while every failure of
  // POST /api/setup/join answers 502. So even once htmx loaded, the entire
  // error path still rendered nothing — an unreachable primary, an expired or
  // reused code, or a mistyped paste all looked identical to a dead button.
  //
  // A Go render test cannot see either bug: the first needs a browser to
  // execute the attribute, the second needs one to perform the swap. This
  // drives the real form with a deliberately bad code and asserts the operator
  // is actually told what went wrong. Runs before the wizard is completed,
  // because /api/setup/join is first-boot-only.
  // Deliberately drives a FAILING request, so it runs in its own page rather
  // than the shared one: the 502 below emits a console error, and the shared
  // watchConsole assertion is checked by every later test in this serial
  // describe. Isolating it here keeps that guard strict for everyone else
  // instead of exempting "502" globally. No session is needed — the join step
  // is first-boot-only, before any login exists.
  test('a bad pairing code reports the error instead of silently doing nothing', async ({ browser }) => {
    const ctx = await browser.newContext();
    const p = await ctx.newPage();
    try {
      await p.goto('/setup');
      await p.locator('button:visible', { hasText: 'Join an existing shop' }).click();

      const msg = p.locator('#setup-join-msg');
      await expect(msg).toBeEmpty();

      await p.locator('input[name="code"]:visible').fill('not-a-real-pairing-code');
      await p.locator('button:visible', { hasText: 'Join' }).click();

      // The operator must SEE a failure. Without the htmx script tag no
      // request is made at all; without the htmx:beforeSwap handler the 502
      // body is dropped. Either way this stays empty and the test fails.
      await expect(msg).not.toBeEmpty({ timeout: 15_000 });
      await expect(msg).toContainText('✗');
    } finally {
      await ctx.close();
    }
  });

  test('completing the wizard creates the admin PIN and logs in', async () => {
    // Step 1 · language — just advance.
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 2 · country (prefills currency/tax client-side).
    await page.locator('select[name=country]').selectOption('GB');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 3 · shop name. setup.html is a standalone document that bypasses
    // web/ui/layouts/base.html (ut-docs#400 review: a first version of the
    // autofill-suppression fix loaded only via that layout and silently
    // missed this page, along with login.html — the exact class of gap
    // ut-docs#344 hit with htmx on this same template). Confirms the real
    // fix (its own <script> tag, per-document) actually runs here.
    const storeName = page.locator('input[name=store_name]');
    await expect(storeName).toHaveAttribute('autocomplete', /^off-/);
    await storeName.fill('E2E Test Shop');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 4 · shop type + sample-data opt-in (ut-docs#539). Pick a type;
    // leave the sample-data checkbox at its unchecked default — the auth
    // project is "a genuinely fresh install", and a fresh install must end
    // up with an empty catalogue unless the operator opts in.
    await page.locator('select[name=shop_type]').selectOption('cafe');
    await expect(page.locator('input[name=demo_data]:visible')).not.toBeChecked();
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 5 · admin PIN.
    await expect(page.locator('input[name=pin]')).toHaveAttribute('autocomplete', /^off-/);
    await page.locator('input[name=pin]').fill('482913');
    await page.locator('input[name=pin_confirm]').fill('482913');
    await page.locator('.setup-nav button:visible', { hasText: 'Next' }).click();

    // Step 6 · finish — real form submit, real redirect to the till.
    await Promise.all([
      page.waitForURL((u) => !u.pathname.includes('/setup')),
      page.locator('button[type=submit]', { hasText: 'Start selling' }).click(),
    ]);
    await expect(page.locator('#basket')).toBeVisible();
  });

  // ut-docs#429. The wizard just provisioned an admin + PIN as real usable
  // state; a genuinely fresh till must be able to open its first shift the
  // same way, on the exact register the wizard's completion step set up —
  // not the shifts.html template's hardcoded fallback option, which used to
  // point at a register row that was never actually inserted (a real
  // browser submitting the real dropdown value is the only thing that
  // catches this — a Go handler test with a hand-picked register_id would
  // never see the template pick the wrong id in the first place).
  test('a fresh till can open its first shift right after the wizard', async () => {
    await page.goto('/shifts');
    await expect(page.locator('#open-shift-form')).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/open')),
      page.locator('#open-shift-form button[type=submit]').click(),
    ]);

    const result = page.locator('#shift-result');
    await expect(result).not.toContainText('500');
    await expect(result).not.toContainText('FOREIGN KEY');

    // A genuine reload (the form's hx-on::after-request triggers one on
    // success) shows the shift as open — not the still-showing Open Shift
    // form, which is what a failed/ignored submit would leave behind.
    await expect(page.locator('body')).toContainText('Shift open since', { timeout: 15_000 });
  });

  // ut-docs#300, checked here because this is the only spec with a real
  // authenticated operator: GET /pin bounces to /login without one, so the
  // default (auth-off) project can never reach this surface. The change-PIN
  // form had the same inline-label defect as the payout dialog -- three
  // password fields in a plain .card that no scoped stylesheet rule covered.
  test('the change-PIN form stacks each label above its own input', async () => {
    await page.goto('/pin');
    await page.waitForSelector('form[action="/api/pin/change"]');

    const fields = await fieldGeometry(page, 'form[action="/api/pin/change"]');
    expect(fields).toHaveLength(3);
    expectStacked(fields, '/pin');

    // Leave the session where the next serial test expects it.
    await page.goto('/');
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
