import { test, expect, Page } from '@playwright/test';
import { ensureOperator } from './helpers';

// ut-docs#2144: hold/resume/scan are all hx-post forms under /api/pos/*,
// and tender is a raw fetch() to the same prefix. Before this fix, a
// session that expired mid-sale left the operator staring at a fully
// rendered sale screen with the generic "Etwas ist schiefgelaufen /
// Something went wrong. Please try again." banner — advice that cannot
// work, since retrying without signing in fails identically forever
// (found on the pilot tablet, ut-docs#2137's device verification).
//
// This needs the `auth` project (real session, real PIN login) — the
// default project's UT_AUTH=off till has no session to expire at all.
// Runs against a bare first-boot till (no demo catalog, per
// ensureOperator's own setup-wizard walkthrough leaving "demo_data"
// unchecked): hold/scan are driven through the real DOM forms, which is
// enough to prove the fix, since the auth middleware short-circuits
// BEFORE the handler (and therefore before any catalog/basket lookup)
// ever runs — session validity is checked first, unconditionally. Resume
// and tender need a held order / a basket respectively, which this bare
// till doesn't have; those two are proven identically at the Go handler
// level (internal/auth/auth_test.go's
// TestMiddlewareHXRequestUnderAPIPathGetsRedirectNotJSON exercises all
// four paths, including these two, against the real Middleware()) and
// here via a real same-origin HTTP request carrying the identical
// HX-Request header htmx itself sends — a real network round trip
// against the real running binary and real revoked session, just not a
// click on a real resume/tender button. Noted explicitly as the boundary
// between "driven via a real UI click" and "driven via a real request
// htmx would send" — not overclaimed either way.
//
// A real, discovered race, handled rather than hidden: the sale screen's
// own background htmx polls (nav chips, basket polls — middleware.go's
// own comment names this class) also carry HX-Request, so the moment the
// session is revoked below, ANY of them can win the redirect before this
// test's own click does. Both orders prove the identical fix; only the
// order is nondeterministic. revokeSessionThenAssertRedirect tolerates
// either.
async function revokeSessionThenAssertRedirect(page: Page, interact: () => Promise<void>) {
  await page.request.post('/api/auth/logout');
  try {
    await interact();
  } catch {
    // A background poll already redirected the page before `interact`
    // could run its own click through to completion — still the fix
    // working, just via a different in-flight request. Fall through to
    // the outcome assertion below either way.
  }
  await page.waitForURL((u) => u.pathname === '/login', { timeout: 5000 });
  await expect(page.locator('#pos-alert')).toBeHidden();
}

test.describe('an expired session mid-sale redirects to /login instead of a generic error banner (ut-docs#2144)', () => {
  test('hold and scan redirect on an expired session; a real request proves resume and tender do too', async ({
    page,
  }) => {
    await ensureOperator(page);
    await page.goto('/');
    await expect(page.locator('#basket')).toBeVisible();

    // hold_api.go's `!d.Engine.HasItems()` guard refuses an empty basket —
    // but that guard runs INSIDE the handler, which the auth middleware
    // must never let a revoked session reach in the first place. So an
    // empty-basket hold submitting at all is exactly the point: if the
    // middleware fix regressed, this would render the generic banner (or,
    // with an empty basket, hold.error.empty) instead of navigating away.
    // "Hold Sale" opens the optional tab-naming dialog first (same
    // two-step flow parked-orders-popup-2137.spec.ts's own parkASale()
    // drives); its own submit button is the real form submit.
    // Addressed by data-testid, not visible text: index.html's own comment
    // on this button (ut-docs#1629) established the convention precisely
    // because matching "Hold Sale" is locale-dependent, and this spec's
    // `auth` till is only English by ensureOperator's wizard walkthrough
    // choosing it — nothing pins it.
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('[data-testid="tender-footer-hold"]').click({ timeout: 2000 });
      await expect(page.locator('#hold-modal')).toBeVisible({ timeout: 2000 });
      await page.locator('#hold-modal button[type=submit]').click({ timeout: 2000 });
    });

    // Re-authenticate for the next real click (the assertion above already
    // proved the navigation; this just gets back to a driveable sale
    // screen for the scan check, same session-per-test shape ensureOperator
    // already establishes for other specs in this file's project).
    await ensureOperator(page);
    await page.goto('/');
    // Same precondition the first block asserts before ITS revoke, and NOT
    // redundant: revokeSessionThenAssertRedirect swallows whatever
    // `interact` throws (the documented background-poll race), so without
    // proving we are actually on a driveable sale screen first, a silently
    // failed re-login would leave the page sitting on /login, make the
    // locator below throw into that catch, and let waitForURL('/login')
    // pass instantly having exercised nothing at all.
    await expect(page.locator('#basket')).toBeVisible();
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('.scan-row input[name="code"]').fill('0000000000000', { timeout: 2000 });
      await page.locator('.scan-row button[type=submit]').click({ timeout: 2000 });
    });

    // Resume and tender: a real same-origin request, carrying the
    // identical HX-Request header htmx sends on every hx-post — against
    // the real running server and a session revoked the same way as
    // above. Uses page.request (Playwright's APIRequestContext, sharing
    // cookies with `page`'s own browser context) rather than
    // page.evaluate+fetch, precisely because of the background-poll race
    // documented above: page.request makes the HTTP call directly,
    // unaffected by whatever `page` itself is doing at the same moment.
    await ensureOperator(page);
    // Prove a REAL session exists before revoking it, or these two
    // assertions would pass identically against a till no one ever signed
    // in to — "no session" and "revoked session" take the same middleware
    // branch, so only this line makes the revocation below load-bearing.
    await page.goto('/');
    await expect(page.locator('#basket')).toBeVisible();
    await page.request.post('/api/auth/logout');
    for (const path of ['/api/pos/resume', '/api/pos/tender']) {
      const resp = await page.request.post(path, { headers: { 'HX-Request': 'true' } });
      expect(resp.status(), `${path} status`).toBe(401);
      expect(resp.headers()['hx-redirect'], `${path} HX-Redirect header`).toBe('/login');
      const bodyText = await resp.text();
      expect(bodyText, `${path} 401 must not carry a JSON body when redirecting`).toBe('');
    }
  });
});
