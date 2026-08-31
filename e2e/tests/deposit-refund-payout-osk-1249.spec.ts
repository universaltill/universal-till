import { test, expect } from './fixtures';
import { setOskMode } from './helpers';

// ut-docs#1249: "after filling the amount and manager pin, the pay out
// button on the deposit refund not working."
//
// Root cause (two compounding bugs in #pfand-amount, both in this fix):
//
// 1. The visible #pfand-amount only ever copied its value into the field
//    actually POSTed (#pfand-amount-minor, name="amount") via `onchange`.
//    osk.js types by mutating .value and firing only `input` — never
//    `change` — and a script-set .value never sets a control's "dirty
//    value" flag, so blur can't synthesize `change` either. On a real
//    touch till (osk.js's default/auto mode) the submitted amount was
//    ALWAYS empty, so every payout 400'd with "amount must be greater
//    than zero" no matter what the operator typed.
// 2. #pfand-amount was type="number", and osk.js's insert() does naive
//    `value += text` for number-type fields (they expose no
//    setRangeText/selection). Typing a decimal point produces a
//    momentarily-invalid float string, and a number input silently
//    resets .value to "" on any invalid intermediate string — so even a
//    fixed onchange/oninput would have captured the WRONG amount for any
//    decimal entry.
//
// A direct Go handler test already covers PfandRueckgabe and always
// passed — that's exactly why this shipped broken: only a real
// virtual-keyboard-driven interaction test (this one) exercises the path
// that was actually failing.

// PfandRueckgabe records the payout against the till's own open shift
// (ut-docs#268) — the fresh e2e till boots with none, so every payout would
// 404 with "shift not found or already closed" regardless of amount unless
// one is opened first. The open-shift form ships a pre-selected register and
// a pre-filled opening-cash value, so a plain submit suffices; unrelated to
// the bug under test, so driven directly rather than via the OSK.
async function ensureShiftOpen(page: import('@playwright/test').Page) {
  await page.goto('/shifts');
  const openForm = page.locator('#open-shift-form');
  if (await openForm.count()) {
    await openForm.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  }
}

async function openPfand(page: import('@playwright/test').Page) {
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('[data-testid="kiosk-pfand-open"]').click();
  await expect(page.locator('#pfand-modal')).toBeVisible();
}

// Presses osk.js's own on-screen keys in sequence — NOT page.fill()/type(),
// which bypass osk.js entirely and would pass even on the unfixed code.
async function typeViaOsk(page: import('@playwright/test').Page, text: string) {
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

test('deposit-refund payout: amount + PIN entered via the on-screen keyboard actually completes', async ({ page }) => {
  await setOskMode(page, 'on');
  await ensureShiftOpen(page);
  await openPfand(page);

  await page.locator('#pfand-amount').click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  await typeViaOsk(page, '2.50');

  // The bug under test: the hidden field actually POSTed must reflect what
  // was typed on-screen, not stay empty.
  await expect(page.locator('#pfand-amount-minor')).toHaveValue('250');
  await expect(page.locator('#pfand-amount')).toHaveValue('2.50');

  await page.locator('[name="manager_pin"]').click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  await typeViaOsk(page, '1234');

  const [response] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/shifts/pfandrueckgabe')),
    page.locator('#pfand-modal button[type="submit"]').click(),
  ]);
  expect(response.status(), 'a correctly-entered amount must complete the payout, not 400').toBe(200);
  await expect(page.locator('#pfand-modal')).toBeHidden();
});

test('deposit-refund payout: a zero amount surfaces a clear error, never a silently-dead button', async ({ page }) => {
  await setOskMode(page, 'on');
  await ensureShiftOpen(page);
  await openPfand(page);

  await page.locator('#pfand-amount').click();
  // "0" is pattern-valid (so the browser's native constraint validation
  // doesn't block the submit before it ever reaches the server) but fails
  // the server's own amount>0 rule — exactly the operator mistake this
  // AC calls out: a clear error, not a button that appears to do nothing.
  await typeViaOsk(page, '0');
  await expect(page.locator('#pfand-amount-minor')).toHaveValue('0');

  const [response] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/shifts/pfandrueckgabe')),
    page.locator('#pfand-modal button[type="submit"]').click(),
  ]);
  expect(response.status()).toBe(400);
  await expect(page.locator('#pfand-result')).toContainText('amount must be greater than zero');
});
