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

// ut-docs#1539 (independent review, BLOCKER): this file's own comment above
// names the exact hazard — "a sync-chip/fiscal-chip wrapping to two lines
// pushes Lock further off-screen" — and ut-docs#1539 does more than wrap
// them: each migrates from a ~21px pill to a full .nav-toggle
// (min-height: 48px, app.css), stacked in the SAME `.nav-right` column as
// the 3 admin links + operator + Lock this file already measures as having
// ZERO headroom. Nothing in this repo had ever rendered either migrated
// chip in a real browser before this test — `scripts/e2e_seed` never
// enrols a till or configures fiscalisation, and the two existing rail-icon
// specs (nav-rail-svg-icons-1423, nav-rail-icon-consistency-1348) target
// specific always-present icons, never iterate the rail, and never trigger
// either chip. This closes that gap for the sync chip (the one item
// reachable from a plain API round-trip in a test — enrolling a till via
// the same two-call flow a real replica uses, no UI pairing dance needed);
// the fiscal chip shares the identical `.nav-toggle` box model (same
// min-height, same icon/badge structure) but needs a live TSE-provisioning
// flow to trigger for real, which is heavier tooling this pass didn't add
// — tracked as a known gap, not silently assumed safe.
test.describe('nav rail Lock button stays reachable with an enrolled-till sync chip present (ut-docs#1539)', () => {
  test('sync chip renders as a nav-toggle and Lock is still a real hit-test target at 1024x600', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await ensureOperator(page);

    // page.request, NOT the top-level `request` fixture: the latter is its
    // own APIRequestContext with no cookies at all, so the manager-gated
    // enroll-token call below would 401 regardless of ensureOperator above
    // (confirmed — that was this test's first failure while drafting it).
    // page.request shares the browser context's cookies, i.e. the real
    // session ensureOperator just established.
    const api = page.request;

    // Mint a real enrolment code the way the Tills page's own QR flow does
    // (POST /api/sync/enroll-token, manager-gated), then complete the
    // enrolment the way a real satellite till would (POST /api/sync/enroll
    // — token-authenticated, no session needed) — no UI pairing dance,
    // but the same two real server calls, not a DB-level shortcut.
    const tokenResp = await api.post('/api/sync/enroll-token');
    expect(tokenResp.ok(), `enroll-token: ${tokenResp.status()} ${await tokenResp.text()}`).toBe(true);
    const tokenHtml = await tokenResp.text();
    const codeMatch = tokenHtml.match(/<code[^>]*>([^<]+)<\/code>/);
    expect(codeMatch, `expected a <code> enrolment string in: ${tokenHtml}`).not.toBeNull();
    const code = codeMatch![1].trim();
    // encodeEnrollCode (sync_api.go) is base64url(JSON({url, token})).
    const decoded = JSON.parse(Buffer.from(code, 'base64url').toString('utf8')) as { token: string };

    const enrollResp = await api.post('/api/sync/enroll', {
      data: { token: decoded.token, name: 'E2E Rail Headroom Till' },
    });
    expect(enrollResp.ok(), `enroll: ${enrollResp.status()} ${await enrollResp.text()}`).toBe(true);
    const enrolled = (await enrollResp.json()).data as { till_id: string };

    await page.setViewportSize({ width: 1024, height: 600 });
    // A fresh navigation, not a soft reload of a page loaded before
    // enrolment — `#sync-chip` only fetches on `hx-trigger="load, ..."`
    // (nav.html), so the chip needs a fresh page load to pick up the newly
    // enrolled till at all.
    await page.goto('/settings');

    // The just-enrolled till has never authenticated (`last_seen_at` is
    // NULL) -> stale -> class=warn -> the badge-carrying, WORST-case box
    // for this measurement, not the quiet ok state.
    const syncLink = page.locator('.sync-chip.warn a.nav-toggle');
    await expect(syncLink).toBeVisible();
    await expect(syncLink.locator('svg[data-icon="sync"]')).toBeVisible();
    await expect(syncLink.locator('.nav-badge')).toBeVisible();

    // Same crowded-rail sanity check as the test above, now WITH the sync
    // chip also present.
    await expect(page.locator('.session-admin-link')).toHaveCount(3);

    const lockBtn = page.locator('.session-lock button.btn-lock');
    await lockBtn.scrollIntoViewIfNeeded();
    const hit = await lockBtn.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });

    // The `auth` project reuses ONE server across every spec FILE in this
    // run, same as the `default` project (playwright.config.ts) — an
    // enrolled till left behind here is real, persistent server state that
    // leaks into whatever spec runs next (confirmed while drafting this:
    // it pushed nav-rail-svg-icons-lock-1423.spec.ts's icon count from 11
    // to 12). Revoke it NOW, still authenticated (revoke is manager-gated,
    // and the Lock click right below ends this session) and BEFORE the hit
    // assertion below, so a failed assertion still leaves the server clean
    // for whatever spec runs next — this test's own job is measuring the
    // rail with the chip PRESENT, not leaving it enrolled afterward.
    const revokeResp = await api.post(`/api/sync/tills/${enrolled.till_id}/revoke`);
    expect(revokeResp.ok(), `revoke: ${revokeResp.status()} ${await revokeResp.text()}`).toBe(true);

    expect(
      hit,
      'Lock must stay a real hit-test target with the migrated sync chip also occupying .nav-right',
    ).toBe(true);

    await Promise.all([
      page.waitForURL((u) => u.pathname === '/login'),
      lockBtn.click(), // no force: must be a genuinely landable click
    ]);
    assertClean();
  });
});
