import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1707: follow-up to ut-docs#1287, which added htmx:sendError
// handlers to three forms (reports_tab_tips.html, refund.html,
// shifts.html) but only wrote a dedicated Playwright test for the
// cheapest of the three to set up (the tips form —
// htmx-senderror-1287.spec.ts). `go test ./internal/pages/...` proves the
// new `{{ T "designer.error.server" }}` calls are valid Go template
// syntax, but that can't catch a typo or logic slip in the inline JS
// itself — only a browser-level test can, and refund.html/shifts.html's
// handlers had none. This file closes that gap for both.
//
// Same defect class throughout: htmx fires `htmx:sendError` (not
// `htmx:responseError`) when a request never gets a response at all —
// e.g. the till dropping off the shop LAN mid-tap (`net::ERR_FAILED`).
// Without a handler for it, the button silently does nothing.

test('refund form: a dropped connection shows a visible message, not a dead button (ut-docs#1287)', async ({
  page,
}) => {
  // route.abort() produces a real network failure, which Chromium's own
  // console also logs — expected noise, not a JS bug (same exemption
  // pattern as htmx-senderror-1287.spec.ts and
  // htmx-admin-error-swap-916.spec.ts). htmx's own error logger prints
  // these two literal strings on this exact failure path; that's htmx's
  // built-in behavior, not a bug this test should fail on.
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);

  // Complete a real cash sale to get a receipt to refund — the same
  // scan -> pay flow sale.spec.ts uses (seeded demo barcode, Coca-Cola Can
  // 330ml).
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();

  // The receipt view's own "Refund" button navigates straight to
  // /refund/<receiptNo> — selecting on the onclick target (not the
  // localized button text) keeps this independent of locale.
  await page.locator('button[onclick*="/refund/"]').click();
  await expect(page.locator('#refund-form')).toBeVisible();

  await page.route('**/api/refund', (route) => route.abort());

  const msg = page.locator('#refund-msg');
  await expect(msg).toBeEmpty();
  await page.locator('#refund-form button[type=submit]').click();
  await expect(msg).not.toBeEmpty();
  // designer.error.server — the same shared fallback key
  // buttons_admin.html's and the tips form's own htmx:sendError handlers
  // already use.
  await expect(msg).toContainText('Something went wrong');

  assertClean();
});

test('shifts form: a dropped connection shows a visible message, not a dead button (ut-docs#1287)', async ({
  page,
}) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);

  // Whichever state the shared till server is currently in (open or no
  // shift), exactly one of #open-shift-form / #close-shift-form is
  // rendered, and both target the same #shift-result with the same
  // document.body htmx:sendError listener this card's acceptance
  // criteria says one representative form is enough to prove.
  await page.goto('/shifts');
  const openForm = page.locator('#open-shift-form');
  const closeForm = page.locator('#close-shift-form');

  await page.route('**/api/shifts/**', (route) => route.abort());

  const result = page.locator('#shift-result');
  await expect(result).toBeEmpty();

  // .fill() dispatches a real 'input' event, so each field's own oninput
  // handler (converting the typed major-unit amount into the hidden
  // minor-unit field the server expects) fires the same as a real tap —
  // no need to poke the hidden field directly.
  if (await openForm.count()) {
    await page.locator('#opening-cash').fill('10.00');
    await openForm.locator('button[type=submit]').click();
  } else {
    await expect(closeForm).toBeVisible();
    await page.locator('#closing-cash').fill('10.00');
    await closeForm.locator('button[type=submit]').click();
  }

  await expect(result).not.toBeEmpty();
  await expect(result).toContainText('Something went wrong');

  assertClean();
});
