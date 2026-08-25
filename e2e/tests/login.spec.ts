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

    // ut-docs#298: setup.html shares .login-logo with login.html — the
    // CANONICAL dark mark with no backing plate (--surface is white in
    // every shipped theme, so it reads cleanly with none). Independent
    // review found the light-glyph-on-a-var(--brand)-plate first draft
    // just relocated the reported defect from white-on-dark to
    // navy-on-white here, on a surface that had no defect to begin with —
    // only .nav (always dark) gets the light variant.
    const logo = page.locator('.login-logo');
    await expect(logo).toHaveAttribute('src', /unitill-logo\.svg/);
    await expect(logo).not.toHaveAttribute('src', /unitill-logo-light\.svg/);
    const bg = await logo.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(bg, 'setup logo must have no backing plate').toBe('rgba(0, 0, 0, 0)');
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
    // ut-docs#617 inserted a new step 5 whose default panel has no "Next"
    // button at all (No / Yes / Later instead) — the old flat click
    // sequence of bare `.setup-nav button:visible` presses would have
    // hunted for a "Next" that isn't there at that point. Scoped to each
    // numbered section (data-step, set on every <section> in setup.html)
    // rather than trying to keep the flat sequence in sync by count.
    const step = (n: number) => page.locator(`[data-step="${n}"]`);

    // Step 1 · language — just advance.
    await step(1).locator('.setup-nav button', { hasText: 'Next' }).click();

    // Step 2 · country (prefills currency/tax client-side).
    await page.locator('select[name=country]').selectOption('GB');
    await step(2).locator('.setup-nav button', { hasText: 'Next' }).click();

    // Step 4 · shop name (step 3, business identity, is Germany-only —
    // ADR-0053/ut-docs#802 — and this flow picks GB, so the wizard jumps
    // from 2 straight to 4). setup.html is a standalone document that bypasses
    // web/ui/layouts/base.html (ut-docs#400 review: a first version of the
    // autofill-suppression fix loaded only via that layout and silently
    // missed this page, along with login.html — the exact class of gap
    // ut-docs#344 hit with htmx on this same template). Confirms the real
    // fix (its own <script> tag, per-document) actually runs here.
    const storeName = page.locator('input[name=store_name]');
    await expect(storeName).toHaveAttribute('autocomplete', /^off-/);
    await storeName.fill('E2E Test Shop');
    await step(4).locator('.setup-nav button', { hasText: 'Next' }).click();

    // Step 5 · shop type + sample-data opt-in (ut-docs#539). Pick a type;
    // leave the sample-data checkbox at its unchecked default — the auth
    // project is "a genuinely fresh install", and a fresh install must end
    // up with an empty catalogue unless the operator opts in.
    await page.locator('select[name=shop_type]').selectOption('cafe');
    await expect(page.locator('input[name=demo_data]:visible')).not.toBeChecked();
    await step(5).locator('.setup-nav button', { hasText: 'Next' }).click();

    // Step 6 · restore from another POS? (ut-docs#617). Drives the Yes ->
    // CSV/Excel sub-picker -> Next path deliberately, not the simpler
    // "No" default: this is the path the review found unreachable (B1 —
    // `choice` doubled as both the panel selector and the answer, so the
    // CSV/Excel panel hid itself the instant its own radio was picked,
    // and re-entering Yes left Next permanently disabled since the radio
    // was already checked and firing no further `change`). Real coverage
    // for the fix, and for the "no detour through Settings/Catalog"
    // acceptance criterion below.
    await step(6).locator('.setup-nav button.secondary', { hasText: 'Yes' }).click();
    const csvPanel = step(6).locator('label.set-row', { hasText: 'CSV' });
    await expect(csvPanel).toBeVisible();
    const nextBtn = step(6).locator('.setup-nav button.primary', { hasText: 'Next' });
    await expect(nextBtn).toBeDisabled();
    await csvPanel.locator('input[type=radio]').check();
    await expect(nextBtn).toBeEnabled();
    await expect(csvPanel).toBeVisible(); // the bug: picking the radio used to hide this whole panel, Next included
    await nextBtn.click();

    // Step 7 · admin PIN.
    await expect(page.locator('input[name=pin]')).toHaveAttribute('autocomplete', /^off-/);
    await step(7).locator('input[name=pin]').fill('482913');
    await step(7).locator('input[name=pin_confirm]').fill('482913');
    await step(7).locator('.setup-nav button', { hasText: 'Next' }).click();

    // Step 8 · finish — real form submit. "CSV/Excel" lands straight in
    // the existing catalog importer instead of home (ut-docs#617 AC: "no
    // detour through Settings/Catalog navigation").
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/import'),
      page.locator('button[type=submit]', { hasText: 'Start selling' }).click(),
    ]);

    // Leave the session on the till's home screen, where the next serial
    // test expects it (same convention as the change-PIN test below).
    await page.goto('/');
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

    // ut-docs#298: same canonical-mark, no-plate check as the setup wizard
    // above, this time on the real /login page.
    const logo = page.locator('.login-logo');
    await expect(logo).toHaveAttribute('src', /unitill-logo\.svg/);
    await expect(logo).not.toHaveAttribute('src', /unitill-logo-light\.svg/);
    const bg = await logo.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(bg, 'login logo must have no backing plate').toBe('rgba(0, 0, 0, 0)');

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
