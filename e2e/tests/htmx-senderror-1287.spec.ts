import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1287: refund.html, shifts.html and reports_tab_tips.html's forms
// only ever handled htmx:responseError (a non-2xx HTTP response) — none
// handled htmx:sendError, the event htmx fires instead when the request
// never gets a response at all (e.g. the back-office tablet dropping off
// the shop LAN mid-tap, `net::ERR_FAILED`). Without a handler, that case
// was a silently dead button: no server fragment to swap in, and nothing
// client-side said so either. Same defect class as ut-docs#1697's fix to
// buttons_admin.html, which is the pattern all three now follow.
//
// This exercises the tips record-payout form (reports_tab_tips.html) —
// the cheapest of the three to set up (no prior sale/shift needed), and
// per the card's own acceptance criteria, one form's coverage is enough to
// prove the fix works; refund.html and shifts.html's handlers are the same
// few lines, added in the same change, following the identical shape.
test('record-payout form: a dropped connection shows a visible message, not a dead button (ut-docs#1287)', async ({
  page,
}) => {
  // route.abort() produces a real network failure (net::ERR_FAILED), which
  // Chromium's console also logs on its own — expected noise, not a JS bug
  // (same exemption pattern as htmx-admin-error-swap-916.spec.ts). htmx.min
  // .js's own onerror path (`fe`) additionally self-logs via console.error
  // for exactly this failure class — its trigger wrapper stamps the event
  // name itself into `detail.error` for both htmx:afterRequest and
  // htmx:sendError on this path, and htmx's internal error logger
  // (`b(r.error)`) prints whatever lands in `detail.error` — so these two
  // literal strings are htmx's own built-in behavior on a network failure,
  // not a bug this test should fail on.
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);

  await page.goto('/reports');
  await page.locator('#report-tab-tips').click();
  await expect(page.locator('#tips-amount')).toBeVisible();

  await page.locator('select[name="cashier_id"]').selectOption({ index: 1 }); // first real worker, whoever seeded this till
  await page.locator('#tips-amount').fill('7.50');
  await expect(page.locator('#tips-amount-minor')).toHaveValue('750');

  await page.route('**/api/reports/worker-allocations', (route) => route.abort());

  const msg = page.locator('#tips-result');
  await expect(msg).toBeEmpty();
  await page.locator('form[hx-post="/api/reports/worker-allocations"] button[type=submit]').click();
  await expect(msg).not.toBeEmpty();
  // designer.error.server — the same shared fallback key
  // buttons_admin.html's own htmx:sendError handler already uses.
  await expect(msg).toContainText('Something went wrong');

  assertClean();
});
