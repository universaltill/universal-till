import { test, expect } from '@playwright/test';
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
  await page.goto('/settings');

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

// The Data card's reset-transactions control (manager-only, the same
// script block covering data-reset/archives/customers/catalog-cleanup/
// export) exercises the client-side validation-error path — a wrong
// confirmation string never reaches the server, the cheapest way to prove
// the error-level branch (role="alert", persists rather than
// auto-expiring) without mutating real data.
test('data-reset confirmation-mismatch error renders role="alert" and persists', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');

  const confirmInput = page.locator('#data-reset-confirm');
  const btn = page.locator('#data-reset-btn');
  await expect(btn).toBeVisible();
  await confirmInput.fill('not-the-right-word');
  await btn.click();

  const notice = page.locator('#data-reset-msg .pos-notice.error');
  await expect(notice).toBeVisible();
  await expect(notice).toHaveAttribute('role', 'alert');
  await expect(notice.locator('.notice-text')).not.toContainText('✗');

  // Errors persist until dismissed (scheduleToastDismiss skips .error) —
  // give the 2.5s auto-expire window a moment to prove it does NOT fire.
  await page.waitForTimeout(3000);
  await expect(notice).toBeVisible();

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
  await page.goto('/settings');

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

  await page.unroute('**/api/data/customers**');
  assertClean();
});
