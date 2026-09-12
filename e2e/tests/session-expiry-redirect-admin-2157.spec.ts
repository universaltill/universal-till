import { test, Page } from '@playwright/test';
import { ensureOperator, openNewItemForm } from './helpers';

// ut-docs#2157: ut-docs#2144 taught the auth middleware to answer an
// htmx-driven (HX-Request: true) /api/* request from an expired session
// with HX-Redirect: /login. A raw fetch() never sends that header, so it
// gets the middleware's OTHER branch instead — a bare 401 JSON envelope
// whose "error" field is the {code,message} OBJECT (internal/auth/
// middleware.go), not the plain string every one of these admin pages'
// own endpoints always answers with. Six admin pages read that field
// straight into a message: plugins.html/catalog.html and the shared
// utPostWithElevation helper (app.js, used by settings.html's
// customer-erase/cleanup-catalog) rendered the literal string
// "[object Object]"; tills.html/bluetooth_devices.html/promotions.html
// silently misread the expired session as an empty result ("no matches",
// "none found") instead of either. All six now redirect to /login on a
// 401 before touching the body at all, the same fix app.js's own
// tender-panel handler already uses (session-expiry-redirect-2144.spec.ts).
//
// This needs the `auth` project (a real session that can actually expire)
// — the default project's UT_AUTH=off till has no session to revoke.
//
// NAMED TO SORT AFTER login.spec.ts (found live in CI, ut-docs#2157
// review fallout): the `auth` project's server is shared across every
// spec file in the project (one process, `workers: 1`), and
// login.spec.ts's own first test requires that server to still be
// genuinely unconfigured when it runs. Playwright runs a project's spec
// files in filename-sort order, and this file's own `ensureOperator()`
// completes the full first-boot setup wizard on its very first call —
// so a filename sorting before "login" (the original name here,
// `admin-session-expiry-redirect-2157.spec.ts`, did exactly that) steals
// that fresh-install state out from under login.spec.ts's first test,
// which then finds itself redirected to `/login` instead of `/setup`.
// Keep this file's name sorting after "login" if it's ever renamed again.
// One representative call site is exercised per distinct mechanism: the
// shared utPostWithElevation helper (covers settings.html AND
// reports_tab_eod.html in one proof), plugins.html's globally-exposed
// window.act, and one real UI-driven click/keystroke per page-local
// closure (catalog/tills/bluetooth_devices/promotions) that cannot be
// reached any other way.
//
// Deliberately NOT using helpers.watchConsole here (unlike most specs in
// this suite): every one of these requests genuinely gets a real 401 back
// — that's the whole point — and Chromium logs "Failed to load resource:
// ... 401" as a console error for that regardless of what the page's own
// JS then does with it. Asserting a clean console would fail on the
// expected 401 itself, not on a regression.
async function revokeSessionThenAssertRedirect(page: Page, interact: () => Promise<void>) {
  await page.request.post('/api/auth/logout');
  try {
    await interact();
  } catch {
    // Tolerate the same background-poll race session-expiry-redirect-2144's
    // own helper documents: base.html's #pairing-notice-mount (hx-trigger
    // "load, every 30s") also carries HX-Request and can win the redirect
    // before `interact` finishes driving its own click/fill. Either way
    // the assertion below proves the fix.
  }
  await page.waitForURL((u) => u.pathname === '/login', { timeout: 5000 });
}

test.describe('admin pages redirect to /login on a session-expired 401 instead of showing [object Object] (ut-docs#2157)', () => {
  test('the shared utPostWithElevation helper redirects (settings.html erase/cleanup-catalog, reports_tab_eod.html)', async ({
    page,
  }) => {
    await ensureOperator(page);
    await page.goto('/settings');
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.evaluate(() => {
        // Real endpoint, bogus id: the auth middleware short-circuits
        // before the handler (and therefore before the id is ever looked
        // at) exactly like session-expiry-redirect-2144's own tender/resume
        // check relies on.
        (window as unknown as { utPostWithElevation: Function }).utPostWithElevation(
          '/api/data/customers/erase',
          { id: 'e2e-nonexistent' },
          () => {},
        );
      });
    });
  });

  test("plugins.html's window.act redirects", async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/plugins');
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.evaluate(() => {
        (window as unknown as { act: Function }).act('/api/plugins/e2e-nonexistent/enable', 'POST');
      });
    });
  });

  test('catalog.html barcode autofill redirects', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/catalog');
    await openNewItemForm(page);
    await page.locator('#item-barcode').fill('0000000000000');
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('#autofill-btn').click({ timeout: 2000 });
    });
  });

  test('tills.html discover-primaries redirects', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/tills');
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('#discover-btn').click({ timeout: 2000 });
    });
  });

  test('bluetooth_devices.html scan redirects', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/bluetooth-devices');
    // #bt-scan-btn is server-side `disabled` when BlueZ itself is
    // unavailable (bluetooth_devices_page.go) — true in this sandbox (no
    // real Bluetooth adapter) regardless of session state, and orthogonal
    // to what this test is proving. Clear it so the click can reach the
    // handler under test, same as forcing any other unrelated
    // precondition out of the way.
    await page.locator('#bt-scan-btn').evaluate((el: HTMLButtonElement) => {
      el.disabled = false;
    });
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('#bt-scan-btn').click({ timeout: 2000 });
    });
  });

  test('promotions.html customer search redirects', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/promotions');
    await revokeSessionThenAssertRedirect(page, async () => {
      await page.locator('#promo-cust-q').fill('anything', { timeout: 2000 });
    });
  });
});
