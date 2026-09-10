import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#918: settings.html's ad-hoc `msg.textContent = '✓/✗/⏳ ' + text`
// client-JS status spans now render the same `.pos-notice` markup as the
// sale screen and catalog.html (ut-docs#213/#238) — a semantic
// role="status"/"alert" element with a dismiss control, not a plain
// unicode-glyph-prefixed text node with no accessible role at all. This
// covers one server-round-trip site (window-mode-form, a plain settings
// save shared by every till project) as a representative of the pattern
// applied across all nine migrated spans in settings.html — the full list
// is verified indirectly by guard-i18n.sh (no hardcoded literal reached
// the page) and go test ./internal/pages/... (handler behaviour
// unchanged); this spec is the layer those can't see: the actual rendered
// DOM shape a screen reader would announce.
test('window-mode save renders a real .pos-notice, not a bare glyph-prefixed text node', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings#settings-display'); // ut-docs#1960: Settings is two-pane now — deep-link to the section this drives

  const msg = page.locator('#window-mode-msg');
  await expect(msg).toBeEmpty();

  await page.locator('#window-mode-form select[name="mode"]').selectOption('normal');
  await page.locator('#window-mode-form button[type="submit"]').click();

  const notice = msg.locator('.pos-notice.success');
  await expect(notice).toBeVisible();
  await expect(notice).toHaveAttribute('role', 'status');
  await expect(notice.locator('.notice-text')).not.toBeEmpty();
  // The old ad-hoc rendering prefixed a raw '✓ ' glyph onto the text node
  // directly; the new markup carries the checkmark via CSS only (a `role`
  // + `.success` class., not text) — the visible text itself must not
  // duplicate it.
  await expect(notice.locator('.notice-text')).not.toContainText('✓');
  await expect(notice.locator('.notice-dismiss')).toHaveAttribute('aria-label', /.+/);

  // Dismiss control actually removes the notice (shared app.js delegated
  // handler, unchanged by this migration but now reachable from this span
  // for the first time).
  await notice.locator('.notice-dismiss').click();
  await expect(msg.locator('.pos-notice')).toHaveCount(0);

  assertClean();
});

// ut-docs#1841 (ADR-0087): the Data card's reset-transactions control
// dropped its typed-word confirmation for step-up manager-PIN
// re-authentication (checkStepUp) — window.utPostWithElevation (app.js)
// opens the shared #elevation-modal dialog instead of a client-side
// validation-error span. This drives that dialog for real: an empty first
// click gets the first-time prompt (no error yet), a wrong PIN re-renders
// the SAME dialog with a real server-side invalid-PIN error — the
// cheapest way to prove the error path without a seeded manager PIN this
// e2e fixture has no way to provide (UT_AUTH=off never needed one before).
test('data-reset now requires a manager PIN step-up, not a typed word (ut-docs#1841)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings#settings-data'); // ut-docs#1960: Settings is two-pane now — deep-link to the section this drives

  const btn = page.locator('#data-reset-btn');
  await expect(btn).toBeVisible();
  await btn.click();

  // First-time prompt: the shared elevation dialog, carrying this action's
  // own translated summary, no error yet.
  const dialog = page.locator('#elevation-modal');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('.elevation-summary')).toBeVisible();
  await expect(dialog.locator('.login-error')).toHaveCount(0);

  // A wrong PIN is a real failed attempt against the server's own
  // AuthorizeManager, not a client-side check — the dialog re-renders
  // with a genuine invalid-PIN error, and stays open (never a terminal
  // success/refusal state).
  await dialog.locator('input[name="override_pin"]').fill('000000');
  await dialog.locator('button[type="submit"]').click();

  await expect(page.locator('#elevation-modal .login-error')).toBeVisible();
  await expect(page.locator('#elevation-modal')).toBeVisible();

  assertClean();
});

// ut-docs#918 review finding 2: an in-flight progress indicator (data
// clearing/restoring/purging/searching/exporting) must NOT be routed
// through renderNotice() — a .pos-notice with level "info"/"success"
// auto-expires after 2.5s (scheduleToastDismiss), which would blank the
// message while a slower-than-2.5s operation is still running and the
// button still disabled. These stay plain text, which only the eventual
// success/error renderNotice() call replaces. cust-msg's search is the
// safest site to drive (read-only, no confirmation dialog, no server-side
// mutation) to prove the progress text survives past 2.5s.
test('customer search progress indicator is plain text and survives past the 2.5s pos-notice auto-expire window', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings#settings-data'); // ut-docs#1960: Settings is two-pane now — deep-link to the section this drives

  const msg = page.locator('#cust-msg');
  const q = page.locator('#cust-q');
  await expect(q).toBeVisible();
  // Stall the response so the progress text has time to be observed —
  // and to still be there past 2.5s, proving no auto-expire fired.
  await page.route('**/api/data/customers**', async (route) => {
    await new Promise((r) => setTimeout(r, 2800));
    await route.continue();
  });
  await q.fill('nobody-matches-this');
  await page.locator('#cust-search-btn').click();

  await expect(msg).toHaveText('…');
  await expect(msg.locator('.pos-notice')).toHaveCount(0);
  await page.waitForTimeout(2600); // past the 2.5s pos-notice auto-expire window
  await expect(msg).toHaveText('…'); // still there — never became a .pos-notice, so nothing dismissed it

  // Let the stalled (2800ms) handler actually continue the request before
  // unrouting: unroute does not wait for an in-flight handler, and on a
  // loaded runner the ~200ms margin above was enough for route.continue()
  // to land after it and throw "Route is already handled" (seen once while
  // driving this spec for ut-docs#1960).
  await page.waitForResponse('**/api/data/customers**');
  await page.unroute('**/api/data/customers**');
  assertClean();
});
