import { test, expect, Page } from '@playwright/test';
import { ADMIN_PIN, watchConsole, fieldGeometry, expectStacked } from './helpers';

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

  // ut-docs#1096: setup.html is a standalone document that was shipping
  // without web/public/osk.js at all — a shop owner on a keyboard-less
  // touchscreen (the exact hardware this ships on) could not even reach
  // the store-name field, let alone complete setup. Static presence
  // (scripts/ci/guard-osk-loaded.sh) and a Go handler test
  // (TestLoginAndSetupLoadOnScreenKeyboard) already prove the script tag
  // ships; only a real browser can prove the keyboard actually opens on
  // a tap, which is the entire point of the fix. Own touch-capable
  // context (like the pairing-code test above), not the shared
  // non-touch `page`, and runs before the shared wizard flow completes
  // first-boot so /setup still renders the fresh-install form.
  test('the on-screen keyboard actually appears when a touch device taps a setup field', async ({ browser }) => {
    const ctx = await browser.newContext({ hasTouch: true });
    const p = await ctx.newPage();
    try {
      await p.goto('/setup');
      await p.locator('[data-step="1"] .setup-nav button', { hasText: 'Next' }).click();
      // ut-docs#1095: country is a tile picker now, not a <select> — the
      // detected tile may already be showing, or "Show all countries" may
      // need a tap first (nothing detected on this headless run's host).
      // Review finding (Blocker 1): wait for step 2 to actually be showing
      // BEFORE the isVisible() check below — isVisible() does not
      // auto-wait, so checking it in the same tick as the click above
      // raced Alpine's x-show and always read false, silently skipping
      // "Show all countries" on any host where a country WAS detected
      // (verified against a DE-detecting till: reproduced 5/5, fixed by
      // this wait 9/9).
      const countryHeading = p.locator('[data-step="2"] h1');
      await countryHeading.waitFor();
      const showAllBtn = p.locator('[data-step="2"] button', { hasText: 'Show all countries' });
      if (await showAllBtn.isVisible()) await showAllBtn.tap();
      await p.locator('[data-step="2"] button.picker-tile[value="GB"]').tap();
      await p.locator('[data-step="2"] .setup-nav button', { hasText: 'Next' }).click();

      const storeName = p.locator('input[name=store_name]');
      await expect(storeName).toBeVisible();
      await storeName.tap();
      await expect(p.locator('#osk')).toBeVisible();
      await expect(p.locator('#osk .osk-key').first()).toBeVisible();
    } finally {
      await ctx.close();
    }
  });

  // ut-docs#1095: the country and shop-type steps were native <select>
  // elements — "so hard" to use on the real touchscreen this ships on
  // (product owner, Pi 5, verbatim). Now a grid of large tappable tiles.
  // Own isolated context, same convention as the two tests above — this
  // must run before the shared wizard flow below completes first-boot,
  // and must not disturb the shared `page`'s own progress through the
  // wizard.
  test('country and shop-type steps are touch tile pickers, not native selects', async ({ browser }) => {
    // 1024x600 is this product's documented kiosk floor (universal-till/
    // CLAUDE.md's offline-first/kiosk ergonomics; reused across e2e specs
    // e.g. basket-no-horizontal-scroll-391.spec.ts).
    const ctx = await browser.newContext({ hasTouch: true, viewport: { width: 1024, height: 600 } });
    const p = await ctx.newPage();
    try {
      await p.goto('/setup');

      // No native <select> anywhere in the wizard — the whole point of
      // ut-docs#1095. Checked page-wide, not just within the two touched
      // steps, so a regression anywhere in the document is caught.
      await expect(p.locator('select')).toHaveCount(0);

      await p.locator('[data-step="1"] .setup-nav button', { hasText: 'Next' }).click();

      const countryStep = p.locator('[data-step="2"]');
      // isVisible() does not auto-wait — wait for the step's own heading
      // first, or this races Alpine's x-show and always reads false (see
      // the on-screen-keyboard test above for the full incident note).
      await countryStep.locator('h1').waitFor();
      const showAllBtn = countryStep.locator('button', { hasText: 'Show all countries' });
      // Whatever this host's locale detection did, "Show all countries"
      // reveals every seeded country once tapped — the built-in list
      // (setup_page.go's builtinSetupCountries) always includes GB.
      if (await showAllBtn.isVisible()) await showAllBtn.tap();
      const gbTile = countryStep.locator('button.picker-tile[value="GB"]');
      await expect(gbTile).toBeVisible();
      // A real touch target, not a collapsed control — same bar
      // helpers.ts's expectStacked applies to labelled form fields.
      const box = await gbTile.boundingBox();
      expect(box?.height ?? 0, 'country tile must be a real, tappable height').toBeGreaterThan(20);

      await gbTile.tap();
      await expect(gbTile).toHaveAttribute('aria-pressed', 'true');
      // Tapping the tile did the old <select>'s @change job: prefilled the
      // hidden currency/tax fields client-side, no server round trip.
      // Alpine's :value binding sets the live DOM property, not the HTML
      // attribute — toHaveValue (not toHaveAttribute) is what actually
      // reads that property, same distinction fieldGeometry-style specs
      // elsewhere in this suite rely on for real form controls.
      await expect(countryStep.locator('input[name=currency]')).toHaveValue('GBP');
      await expect(countryStep.locator('input[name=currency_touched]')).toHaveValue('1');

      // Keyboard operability is not a regression from the native <select>:
      // a real <button> tile is independently focusable and Enter-activates,
      // same as the France tile below proves by actually switching country.
      const frTile = countryStep.locator('button.picker-tile[value="FR"]');
      await frTile.focus();
      await p.keyboard.press('Enter');
      await expect(frTile).toHaveAttribute('aria-pressed', 'true');
      await expect(gbTile).toHaveAttribute('aria-pressed', 'false');
      await expect(countryStep.locator('input[name=currency]')).toHaveValue('EUR');

      await countryStep.locator('.setup-nav button', { hasText: 'Next' }).click();
      await p.locator('input[name=store_name]').fill('Tile Picker Test Shop');
      await p.locator('[data-step="4"] .setup-nav button', { hasText: 'Next' }).click();

      // Shop-type step: all six ADR-0026 tiles shown directly, no "show
      // all" toggle needed for a list this short.
      const shopTypeStep = p.locator('[data-step="5"]');
      await expect(shopTypeStep.locator('button.picker-tile')).toHaveCount(6);
      const cafeTile = shopTypeStep.locator('button.picker-tile[data-value="cafe"]');
      await cafeTile.tap();
      await expect(cafeTile).toHaveAttribute('aria-pressed', 'true');
      await expect(shopTypeStep.locator('input[name=shop_type]')).toHaveValue('cafe');
    } finally {
      await ctx.close();
    }
  });

  // ut-docs#1095 review finding (Blocker 1): the test above only ever
  // exercises the "nothing detected, every tile already showing" branch —
  // this CI host's OS locale never matches a seeded country, so
  // showAllCountries starts true and "Show all countries" is never even
  // rendered. That left the card's actual headline behaviour (a detected
  // country shown ALONE at first, with an explicit toggle to see the rest)
  // with no real coverage at all. Real OS locale detection can't be forced
  // from here either (the till reads it from the process environment at
  // Go-detectCountry time, not from anything Playwright controls) — but the
  // PIN-error re-render path already re-renders with detectedCountry set to
  // whatever country the operator just submitted (setup_page.go's
  // renderWizard, POST branch — the same mechanism
  // TestSetupWizardPINErrorRerenderKeepsOperatorCountryNotDetected covers
  // server-side), which deterministically reproduces the exact same
  // showAllCountries=false initial state a real detection would.
  test('a detected country shows alone at first, with an explicit toggle to see the rest', async ({ browser }) => {
    const ctx = await browser.newContext({ hasTouch: true, viewport: { width: 1024, height: 600 } });
    const p = await ctx.newPage();
    try {
      const step = (n: number) => p.locator(`[data-step="${n}"]`);
      await p.goto('/setup');
      await step(1).locator('.setup-nav button', { hasText: 'Next' }).click();
      await step(2).locator('h1').waitFor();
      // Nothing detected yet on this host, so every tile already shows —
      // pick GB deliberately, same as any operator would.
      await step(2).locator('button.picker-tile[value="GB"]').tap();
      await step(2).locator('.setup-nav button', { hasText: 'Next' }).click();
      await p.locator('input[name=store_name]').fill('Rerender Test Shop');
      await step(4).locator('.setup-nav button', { hasText: 'Next' }).click();
      await step(5).locator('.setup-nav button', { hasText: 'Next' }).click();
      await step(6).locator('.setup-nav button.primary', { hasText: 'No' }).click();
      // A deliberately mismatched PIN forces a server re-render with
      // detectedCountry='GB' (the operator's own prior pick, per the
      // renderWizard POST branch) — never a real detection, but the exact
      // same initial x-data shape a real one would produce.
      await step(7).locator('input[name=pin]').fill('1234');
      await step(7).locator('input[name=pin_confirm]').fill('9999');
      // Step 7's "Next" only advances Alpine's `step` to 8 client-side —
      // the actual POST (and therefore the mismatch validation) fires on
      // step 8's real form submit button.
      await step(7).locator('.setup-nav button', { hasText: 'Next' }).click();
      await step(8).locator('button[type=submit]', { hasText: 'Start selling' }).click();
      await p.waitForLoadState('networkidle');
      await expect(p.locator('.login-error')).toContainText(/PIN/i);

      // The re-rendered page opens on the PIN step (errStep=7); walk back
      // to the country step through the real Back buttons — Alpine state,
      // not a URL — so this exercises the actual navigation path, not a
      // shortcut into internal state.
      await step(7).locator('.setup-nav button', { hasText: 'Back' }).click(); // -> restore (6)
      // Step 6 has TWO "Back" buttons in the DOM (the default panel's and
      // the "Yes, restoring" sub-panel's, x-show-toggled) — :visible scopes
      // to the one actually shown, avoiding a strict-mode violation.
      await step(6).locator('.setup-nav button:visible', { hasText: 'Back' }).click(); // -> shop type (5)
      await step(5).locator('.setup-nav button', { hasText: 'Back' }).click(); // -> shop name (4)
      await step(4).locator('.setup-nav button', { hasText: 'Back' }).click(); // -> country (2), GB isn't DE

      await step(2).locator('h1').waitFor();
      const gbTile = step(2).locator('button.picker-tile[value="GB"]');
      const showAllBtn = step(2).locator('button', { hasText: 'Show all countries' });

      // The headline behaviour: ONLY the previously-picked country tile is
      // visible, pre-selected, plus the toggle — not the whole list. x-show
      // hides everything else via display:none but the elements stay IN
      // the DOM, so a plain toHaveCount() on button.picker-tile would still
      // count all 14 regardless of visibility — :visible scopes it to what
      // is actually shown, same as everywhere else this test checks state.
      await expect(gbTile).toBeVisible();
      await expect(gbTile).toHaveAttribute('aria-pressed', 'true');
      await expect(showAllBtn).toBeVisible();
      await expect(step(2).locator('button.picker-tile[value="FR"]')).toBeHidden();
      await expect(step(2).locator('button.picker-tile:visible')).toHaveCount(1);

      // Tapping the toggle reveals the rest, GB retains its selection at
      // its normal (alphabetical) position — not duplicated, not lost. Not
      // a hardcoded count: assert "more than the single pinned tile", not a
      // magic number that would drift with the seeded country list.
      await showAllBtn.tap();
      // expect.poll, not a one-shot .count() — Alpine's re-render after the
      // tap is not guaranteed to have landed in the same tick the tap
      // resolves, and .count() (unlike expect(locator).toHaveCount()) does
      // not auto-retry.
      await expect
        .poll(() => step(2).locator('button.picker-tile:visible').count(), {
          message: 'expanding must reveal more than just the pinned tile',
        })
        .toBeGreaterThan(1);
      await expect(gbTile).toBeVisible();
      await expect(gbTile).toHaveAttribute('aria-pressed', 'true');
      await expect(showAllBtn).toBeHidden();
    } finally {
      await ctx.close();
    }
  });

  // ut-docs#1126: setup.html was a "standalone document" (own <head>, never
  // extends base.html — see that file's own comment), so it never got
  // ut-docs#161's viewport-fluid root font-size — it hardcoded the OLD,
  // pre-#161 mechanism instead (a FIXED px value, `uiscalepx`, unaffected by
  // viewport size at all). app.css's `--fluid-fs` clamp resolves to ~17px at
  // the 1024x600 kiosk floor and ~20px at 1920x1200 (this panel's exact
  // reported hardware, ut-docs#1126's own measurement).
  //
  // A single test driving BOTH viewports (not two independent ones) is
  // deliberate: independently asserting "!= 16" at each size would still
  // pass for a broken fix that hardcodes any OTHER constant (e.g.
  // `--ui-scale` wired to a literal instead of the template value) —
  // that's not viewport-responsive either, just wrong in a different way.
  // Asserting the two readings actually DIFFER from each other is the part
  // a fixed value, whatever its constant, can never satisfy.
  test("the setup wizard's root scales with the viewport, not a fixed size", async ({ browser }) => {
    const measure = async (viewport: { width: number; height: number }) => {
      const ctx = await browser.newContext({ hasTouch: true, viewport });
      const p = await ctx.newPage();
      try {
        await p.goto('/setup');
        const rootFontSize = parseFloat(await p.evaluate(() => getComputedStyle(document.documentElement).fontSize));

        await p.locator('[data-step="1"] .setup-nav button', { hasText: 'Next' }).click();
        const countryStep = p.locator('[data-step="2"]');
        await countryStep.locator('h1').waitFor();
        const showAllBtn = countryStep.locator('button', { hasText: 'Show all countries' });
        if (await showAllBtn.isVisible()) await showAllBtn.tap();
        // :visible, not just .first() — setup.html keeps every country tile
        // in the DOM under x-show (display:none when hidden), so a plain
        // DOM-order .first() can resolve to a permanently hidden tile on a
        // host where OS-locale detection picks a non-alphabetically-first
        // country (showAllCountries starts false there — see the "detected
        // country" test above). toBeVisible() also auto-retries past the
        // tap-to-Alpine-re-render race the same test's own comment warns
        // about, unlike a one-shot boundingBox() on an unsettled element.
        const tile = countryStep.locator('button.picker-tile:visible').first();
        await expect(tile).toBeVisible();
        const box = await tile.boundingBox();
        return { rootFontSize, tileHeight: box?.height ?? 0 };
      } finally {
        await ctx.close();
      }
    };

    const kiosk = await measure({ width: 1024, height: 600 });
    const waveshare = await measure({ width: 1920, height: 1200 });

    expect(kiosk.rootFontSize, 'root font-size at 1024x600 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size at 1920x1200 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size must actually respond to viewport size, not be a fixed value at both').toBeGreaterThan(kiosk.rootFontSize);

    // 44px is the same touch-target minimum the sale screen holds to after
    // ut-docs#161. Note: .picker-tile's own 3.2rem min-block-size already
    // clears 44px even at the old fixed 16px root, so this doesn't by
    // itself regression-guard the scaling fix — the font-size assertions
    // above are what do that; this just documents the minimum still holds.
    expect(kiosk.tileHeight, 'country tile at 1024x600 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
    expect(waveshare.tileHeight, 'country tile at 1920x1200 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
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

    // Step 2 · country (prefills currency/tax client-side). Tile picker,
    // not a <select> (ut-docs#1095) — reveal the full list if the detected
    // tile alone is showing, then tap GB. isVisible() doesn't auto-wait —
    // wait for the step's own heading first (see the on-screen-keyboard
    // test above for the full incident note).
    await step(2).locator('h1').waitFor();
    const showAllCountriesBtn = step(2).locator('button', { hasText: 'Show all countries' });
    if (await showAllCountriesBtn.isVisible()) await showAllCountriesBtn.click();
    await step(2).locator('button.picker-tile[value="GB"]').click();
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

    // Step 5 · shop type + sample-data opt-in (ut-docs#539). Pick a type
    // (tile picker, not a <select> — ut-docs#1095); leave the sample-data
    // checkbox at its unchecked default — the auth project is "a genuinely
    // fresh install", and a fresh install must end up with an empty
    // catalogue unless the operator opts in.
    await step(5).locator('button.picker-tile[data-value="cafe"]').click();
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

  // ut-docs#1126: login.html has the exact same stale-viewport-scaling gap
  // as setup.html (same standalone-document class, same old fixed
  // `uiscalepx` mechanism) — reporter's "login especially" note when the
  // wizard bug was filed. Own cookie-less context per viewport: the shared
  // serial `page` is mid-session (locked, not logged out), and this only
  // needs the PIN pad to render, not a real session. Requires the wizard to
  // have already run (needs a configured PIN so /login renders the PIN pad
  // rather than the first-boot form) — placed after that point in the file.
  //
  // One test driving both viewports, not two independent ones — same
  // reasoning as the setup-wizard version above: asserting the two
  // readings actually DIFFER is what a fixed value (whatever constant it
  // hardcodes) can never satisfy, where two separate "!= 16" checks could
  // both still pass against a differently-broken fix.
  test("the login screen's root scales with the viewport, not a fixed size", async ({ browser }) => {
    const measure = async (viewport: { width: number; height: number }) => {
      const ctx = await browser.newContext({ hasTouch: true, viewport });
      const p = await ctx.newPage();
      try {
        await p.goto('/login');
        await expect(p.locator('.pin-pad')).toBeVisible();
        const rootFontSize = parseFloat(await p.evaluate(() => getComputedStyle(document.documentElement).fontSize));
        const box = await p.locator('.pin-key').first().boundingBox();
        return { rootFontSize, keyHeight: box?.height ?? 0 };
      } finally {
        await ctx.close();
      }
    };

    const kiosk = await measure({ width: 1024, height: 600 });
    const waveshare = await measure({ width: 1920, height: 1200 });

    expect(kiosk.rootFontSize, 'root font-size at 1024x600 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size at 1920x1200 must not be the old fixed 16px').not.toBe(16);
    expect(waveshare.rootFontSize, 'root font-size must actually respond to viewport size, not be a fixed value at both').toBeGreaterThan(kiosk.rootFontSize);

    expect(kiosk.keyHeight, 'PIN key at 1024x600 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
    expect(waveshare.keyHeight, 'PIN key at 1920x1200 must meet the 44px touch-target minimum').toBeGreaterThanOrEqual(44);
  });

  // ut-docs#1099: exit-to-OS used to live only on /settings — BEHIND the
  // sign-in gate — so a kiosk till sitting at the login screen had no escape
  // at all. The escape hatch now lives on /login itself, session-free (the
  // endpoint's own live manager-PIN check is the gate; the auth middleware
  // exempts it — TestExitToOSReachableWithoutSessionCookie pins the server
  // side). Only a real browser proves the full path: the collapsed
  // disclosure opens, the fetch actually fires without a session cookie,
  // and the operator SEES the outcome. Own cookie-less context (the "no
  // session" property is the point), so the shared serial page's login
  // state is neither used nor disturbed; own watchConsole with the
  // non-2xx exemption, since both submissions below deliberately drive
  // real 4xx/5xx responses Chromium logs as console errors regardless of
  // how the page handles them (see helpers.ts's extraExempt note).
  test('the login screen itself offers a PIN-gated exit to OS, without a session', async ({ browser }) => {
    const ctx = await browser.newContext();
    const p = await ctx.newPage();
    const assertOwnClean = watchConsole(p, /^Failed to load resource: .*(403|429|503)/);
    try {
      await p.goto('/login');
      // Genuinely session-less: the PIN pad (not a redirect to /) shows.
      await expect(p.locator('.pin-pad')).toBeVisible();

      // Collapsed by default — a normal login stays uncluttered.
      const details = p.locator('details.login-exit-os');
      const form = p.locator('#login-exit-os-form');
      await expect(details.locator('summary')).toBeVisible();
      await expect(form).toBeHidden();
      await details.locator('summary').click();
      await expect(form).toBeVisible();

      const msg = p.locator('#login-exit-os-msg');
      const pinField = form.locator('[name="manager_pin"]');

      // A wrong PIN must be visibly rejected (403 — or 429 if earlier
      // specs already burned lockout budget; both render the same
      // rejection). One attempt only: the failure count is shared
      // device-wide with keypad login (5 failures = 30s lockout).
      await pinField.fill('999999');
      await form.locator('button[type=submit]').click();
      await expect(msg).toContainText('Incorrect PIN');

      // The real admin PIN passes BOTH gates (middleware exemption + the
      // handler's own AuthorizeManager). This e2e server has no desktop
      // shell attached, so the honest outcome is the 503 no_shell class —
      // "window can't be reached", explicitly NOT the PIN rejection above.
      // (Same no-shell expectation the Go-side
      // TestExitToOSEndpoint_NoShellAttached503NoAudit codifies.)
      await pinField.fill(ADMIN_PIN);
      await form.locator('button[type=submit]').click();
      await expect(msg).toContainText("can't be reached");
      await expect(msg).not.toContainText('Incorrect PIN');
      assertOwnClean();
    } finally {
      await ctx.close();
    }
  });
});
