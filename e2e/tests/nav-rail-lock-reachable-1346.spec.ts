import { test, expect } from '@playwright/test';
import { ensureOperator, watchConsole } from './helpers';

// ut-docs#1346: independent review (universal-till PR #670, ut-docs#1332's
// nav-rail change) simulated the real session_chip.html markup (3 manager
// admin links + operator + Lock) on /settings at 1024x600 and measured
// `.nav` scrollHeight 614 vs clientHeight 600 (overflows by 14px) — Lock's
// own bottom edge sits ~4px below the rail's clientHeight. Not broken today
// (the rail is `overflow-y: auto`, app.css, and Lock stays hit-testable at
// its centre), but with zero headroom: one more rail item (a plugin nav
// entry, or a sync-chip/fiscal-chip wrapping to two lines) pushes Lock
// further off-screen on the most common kiosk resolution, with nothing here
// to catch it. This is that regression guard, same pattern
// tender-panel-reachable.spec.ts already uses for the tender panel: a real
// hit-test (not a bounding-box/isVisible check, which passes even for an
// element sitting inside a collapsed or scrolled-off ancestor) backed by a
// real, completing click — not a forced one.
//
// This spec needs a REAL manager session, not just a reachable page: the
// `#session-chip` fragment (the 3 admin links + operator + Lock this test
// measures) only renders once `auth.FromContext` resolves a real session
// cookie (auth_page.go's `GET /ui/session-chip`), and `auth.Middleware` —
// the only thing that ever populates that context — is never installed at
// all when UT_AUTH=off (internal/pages/init.go). So the default project's
// canPerform() bypass (which is enough for every OTHER admin page since
// ut-docs#901/#902) does not help here: the chip renders empty regardless.
// Confirmed live (see this file's own history) — 0 `.session-admin-link`
// elements on the default project's /settings, not 3. This is why the file
// is routed to the `auth` project (playwright.config.ts's AUTH_ONLY_SPECS)
// instead, using `ensureOperator` the same way login.spec.ts and the
// /tables specs do — a fresh browser context here has no cookie, so it PIN-
// logs in against whatever admin operator login.spec.ts's wizard already
// created (this file runs after login.spec.ts in file-sort order, verified
// with `playwright test --project=auth --list`), isolated from that file's
// own shared page/session.
test.describe('nav rail Lock button stays reachable at 1024x600 with a full manager session (ut-docs#1346)', () => {
  test('Lock is a real hit-test target and completes logout', async ({ page }) => {
    const assertClean = watchConsole(page);
    await ensureOperator(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings');

    // Sanity check this is actually the crowded rail the review measured:
    // 3 manager admin links (Users/Promotions/Translations) + operator +
    // Lock in `.nav-right`, on top of Till/Menu/Inventory/Orders in
    // `.nav-primary` above it.
    await expect(page.locator('.session-admin-link')).toHaveCount(3);

    const lockBtn = page.locator('.session-lock button.btn-lock');
    // `.nav` is `overflow-y: auto` — scroll it into view first, matching
    // the tab-panel test's own pattern for a scrollable ancestor, rather
    // than asserting no-scroll-needed the way the always-visible
    // tender-footer tests do.
    await lockBtn.scrollIntoViewIfNeeded();
    const hit = await lockBtn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(
      hit,
      'Lock must be the real hit-test target, not clipped by the rail running out of headroom',
    ).toBe(true);

    // A real click completing a real logout is the strongest proof, same
    // reasoning tender-panel-reachable.spec.ts documents: it fails if the
    // button is present-but-unclickable in any way a geometry check alone
    // can't see. Safe against login.spec.ts's own shared session: this
    // test's browser context (and cookie) is its own, from `ensureOperator`
    // above, never the one login.spec.ts's serial block carries forward.
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/login'),
      lockBtn.click(), // no force: must be a genuinely landable click
    ]);
    assertClean();
  });
});
