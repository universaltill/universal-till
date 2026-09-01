import { test, expect } from './fixtures';
import { setOskMode, watchConsole } from './helpers';

// ut-docs#1385: #payment-overlay opened via showModal(), which promotes it
// to the browser's native "top layer" and makes everything outside it
// `inert` for as long as it's open — including the custom on-screen
// keyboard (#osk), a singleton appended once to <body>, never re-parented
// into whichever dialog is currently open. #osk still rendered (confirmed
// live: getBoundingClientRect()/getComputedStyle both showed it correctly
// laid out, on-screen), but a real tap on it did not register — it was
// inert, not merely visually behind something. A geometry/visibility
// assertion alone would pass against that broken build (the keyboard IS
// visible), which is exactly why this drives a real click at the actual
// key and asserts the target field's VALUE changed, mirroring
// deposit-refund-osk-1248.spec.ts's own "prove the click really lands"
// standard rather than simulating osk.js's insert() logic directly.
//
// Fix: the overlay now opens non-modally (.show()), the same fix already
// applied to #hold-modal/#pfand-modal/#elevation-modal/#table-add-modal for
// the identical reason (see app.css's #hold-modal comment) — #osk's
// z-index (1000) stays above the overlay's own (500, matching those four)
// so the keyboard renders on top of whichever Split-tab field it types
// into. Deliberately targets the `reference` field (a plain type="text"
// input, name="reference") rather than `amount`/`change` — those two are
// ut-docs#1284's separate decimal-corruption scope (type="number" today);
// this card's fix is about tappability, not field type, so picking a field
// untouched by that other in-flight card keeps this spec's pass/fail
// independent of it.
test.describe('payment overlay: the on-screen keyboard stays tappable while it is open (ut-docs#1385)', () => {
  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a real OSK key click reaches the Split tab reference field and inserts text', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await page.locator('.tender .tab').nth(1).click(); // Split tab
    await expect(page.locator('#split-tender-card')).toBeVisible();

    const reference = page.locator('#split-tender-form input[name="reference"]');
    // Never opened at all before a deliberate tap (same singleton contract
    // deposit-refund-osk-1248.spec.ts already pins for #pfand-modal).
    expect(await page.locator('#osk').count()).toBe(0);

    await reference.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();

    // The regression itself: before the fix, this click landed on nothing
    // (the overlay's showModal() made #osk inert) and the value stayed
    // empty. `{ force: true }` is deliberately NOT used — a forced click
    // bypasses Playwright's actionability/hit-testing check and would pass
    // against the broken build too, exactly the false-pass this spec exists
    // to rule out.
    for (const ch of 'ab12') {
      await page.locator(`#osk button[data-k="${ch}"]`).click();
    }
    await expect(reference).toHaveValue('ab12');

    // The field stays reachable and editable while the overlay is still
    // open — this bug made the whole overlay's OSK-hosting fields
    // unusable, not just the first character.
    await page.locator('#osk button[data-k="⌫"]').click();
    await expect(reference).toHaveValue('ab1');

    assertClean();
  });

  test('closing the overlay hides the keyboard along with it', async ({ page }) => {
    await setOskMode(page, 'on');
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab').nth(1).click();
    await page.locator('#split-tender-form input[name="reference"]').click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();

    await page.getByTestId('payment-close').click();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();
    await expect(page.locator('#osk.osk-open')).not.toBeVisible();
  });
});

// Independent review of this same card's first draft found a SECOND,
// self-inflicted regression the tests above don't catch: making #osk
// tappable (above) also means #osk can now sit ON TOP OF the overlay's own
// Complete Sale/Clear buttons, since #osk (z-index 1000) draws over
// whatever .payment-overlay's fixed 100dvh height put underneath it —
// body.osk-padded's 15.5rem body-padding reservation (app.css) does
// nothing for a position:fixed element sized against the viewport, not
// body's padding box. Measured live pre-fix at 1024x600: #split-tender-
// submit at y 436-487, #osk spanning y 312-600 — genuinely, unreachably
// covered, not merely close to an edge. Fixed by having
// body.osk-padded .payment-overlay clamp its own height, which lets
// #split-tender-card's EXISTING `flex: 1; overflow-y: auto` (the same
// floor ut-docs#161's review already established for this exact collapse
// class) take over: the panel now scrolls to reach the buttons instead of
// them being rendered under the keyboard.
//
// Real end-to-end proof, not a geometry check: types a reference (opening
// the OSK, exactly the "typed a note right before paying" sequence the
// review reproduced), scrolls, hit-tests, then completes an ACTUAL sale
// with a real, unforced click — the same standard tender-panel-reachable.
// spec.ts already holds Cash/Card/New-Customer to. Covers both the
// reviewer's own reference kiosk viewport (1024x600) and Playwright's
// default (1280x720, what this file's OTHER tests above run at) since the
// review found the failure reproduces at both.
test.describe('payment overlay: Complete Sale stays reachable with the keyboard open (ut-docs#1385 review)', () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of [
    { width: 1024, height: 600 },
    { width: 1280, height: 720 },
  ]) {
    test(`Complete Sale is a real hit target and completes a sale at ${vp.width}x${vp.height}`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await setOskMode(page, 'on');
      await page.setViewportSize(vp);
      await page.goto('/');
      await page.waitForSelector('.pos-container');

      await page.getByRole('textbox').first().fill('5000000000012');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
        page.locator('.scan-row button[type=submit]').click(),
      ]);
      await expect(page.locator('#basket')).toContainText('Coca-Cola');

      await page.getByTestId('payment-open').click();
      await page.locator('.tender .tab').nth(1).click(); // Split tab
      await page.locator('#split-tender-form select[name="method"]').selectOption({ index: 0 });
      await page.locator('#split-tender-form input[name="amount"]').fill('1.40');
      await page.locator('#split-tender-form input[name="change"]').fill('0.20');
      await page.locator('#split-tender-add').click();
      await page.waitForSelector('.payment-pill');

      // Type the reference LAST, as an operator would right before paying —
      // this is what leaves the OSK open while reaching for Complete Sale,
      // the exact sequence the review reproduced. Nothing after this click
      // shifts focus, so the keyboard stays up.
      await page.locator('#split-tender-form input[name="reference"]').click();
      await expect(page.locator('#osk.osk-open')).toBeVisible();

      // expect.poll, not a one-shot check: adding the payment pill just
      // above reflows #split-tender-card's content (its own height, and
      // therefore how far this button sits from the fold), and that
      // reflow isn't guaranteed to have fully settled the instant
      // waitForSelector('.payment-pill') resolves -- observed directly: a
      // one-shot elementFromPoint immediately after scrollIntoViewIfNeeded
      // occasionally raced it and read a stale, pre-reflow layout. Polling
      // re-scrolls and re-measures each attempt, so it can't have this
      // false-negative failure mode; it can still catch the real
      // regression (the button never becomes reachable, poll times out).
      const submit = page.locator('#split-tender-submit');
      await expect
        .poll(async () => {
          await submit.scrollIntoViewIfNeeded();
          return submit.evaluate((el) => {
            const r = el.getBoundingClientRect();
            const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
            return !!at && (at === el || el.contains(at));
          });
        }, 'Complete Sale must be a real hit-test target, not covered by the open keyboard')
        .toBe(true);

      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
        submit.click(), // no force: must be a genuinely landable click
      ]);
      await expect(page.locator('#basket.receipt-view')).toBeVisible();
      assertClean();
    });
  }
});
