import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2224 / ADR-0098: the persistent app shell. The Go guards in
// internal/pages/persistent_shell_test.go prove the markup/headers exist;
// this file proves the BEHAVIOUR in a real Chromium: a rail tap keeps the
// document (no CSS/JS re-parse, no Alpine re-boot), syncs what lives
// outside the swapped region, never stacks a page's listeners, never
// snapshots page content into localStorage, and falls back to a full
// document load for anything that is not a page of this shell.

const bootAt = (page: import('@playwright/test').Page) => page.evaluate(() => (window as any).UT.shellBootAt as number);

test.describe('persistent app shell (ut-docs#2224)', () => {
  test('a rail tap keeps the document and fetches only the page', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const boot = await bootAt(page);
    expect(boot).toBeGreaterThan(0);
    await expect(page.locator('body')).toHaveClass(/sale-screen/);
    await page.evaluate(() => performance.clearResourceTimings());

    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await expect(page.locator('body')).not.toHaveClass(/sale-screen/);

    expect(await bootAt(page), 'the document must survive a boosted navigation').toBe(boot);
    const fetched = await page.evaluate(() =>
      performance.getEntriesByType('resource').map((e) => new URL(e.name).pathname)
    );
    // ut-docs#2363: Chromium refetches the <link rel="icon"> target itself
    // on every history.pushState() navigation (a browser-internal favicon
    // probe, resourceTiming initiatorType "other") — verified this is not
    // triggered by any app JS (nothing touches the <link> or <head>) and is
    // not a caching gap: forcing `Cache-Control: public, max-age=31536000,
    // immutable` on the response still doesn't stop it, so it bypasses the
    // renderer's HTTP cache entirely. Excluded here as a known, accepted
    // exception to the guarantee below rather than something fixable
    // app-side; every other asset must still never re-fetch.
    const FAVICON_PATH = '/public/assets/logo/ut-logo.ico';
    expect(
      fetched.filter((p) => (p.startsWith('/public/') || p.startsWith('/themes/')) && p !== FAVICON_PATH),
      'no asset may be re-fetched on navigation (except the browser\'s own favicon probe, ut-docs#2363)'
    ).toEqual([]);
    // `#pairing-notice-mount` and the rail chips are hx-preserve'd, so the
    // navigation is the page itself plus (at most) the input heartbeat and
    // ut-docs#2343's theme-sync poll (deliberately not preserved: its OOB
    // response carries its own id).
    expect(fetched.filter((p) => p.startsWith('/ui/') && p !== '/ui/theme-sync')).toEqual([]);
    expect(await page.title()).toBe('Menu');
    assertClean();
  });

  test('a page\'s document-level listeners are dropped when it is swapped away', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      const w = window as any;
      w.__ll = { add: 0, rem: 0 };
      const a = EventTarget.prototype.addEventListener, r = EventTarget.prototype.removeEventListener;
      const tracked = (t: EventTarget) => t === document.body || t === document || t === window;
      EventTarget.prototype.addEventListener = function (type: string, fn: any, o?: any) {
        if (tracked(this) && type === 'htmx:afterSwap') w.__ll.add++;
        return a.call(this, type, fn, o);
      };
      EventTarget.prototype.removeEventListener = function (type: string, fn: any, o?: any) {
        if (tracked(this) && type === 'htmx:afterSwap') w.__ll.rem++;
        return r.call(this, type, fn, o);
      };
    });
    await page.goto('/');
    const boot = await bootAt(page);
    const roundTrip = async () => {
      await page.locator('[data-testid="nav-menu"]').click();
      await expect(page.locator('body')).toHaveClass(/menu-screen/);
      await page.locator('[data-testid="nav-till"]').click();
      await expect(page.locator('body')).toHaveClass(/sale-screen/);
    };
    await roundTrip();
    const after1 = await page.evaluate(() => (window as any).__ll as { add: number; rem: number });
    await roundTrip();
    await roundTrip();
    const after3 = await page.evaluate(() => (window as any).__ll as { add: number; rem: number });
    expect(await bootAt(page)).toBe(boot);
    // index.html registers body-level afterSwap handlers in its inline
    // scripts; every departure from Sell must remove exactly what the
    // arrival added, so the live count is flat, not growing per visit.
    expect(after3.add - after3.rem).toBe(after1.add - after1.rem);
    expect(after3.rem).toBeGreaterThan(after1.rem);
    assertClean();
  });

  test('no page content is snapshotted into localStorage; back is a full reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.evaluate(() => localStorage.setItem('htmx-history-cache', '[{"url":"/x","content":"<p>stale</p>"}]'));
    // The shell purges an older build's snapshots on the next boosted swap.
    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    expect(await page.evaluate(() => localStorage.getItem('htmx-history-cache'))).toBeNull();
    const boot = await bootAt(page);
    await page.goBack();
    await page.waitForLoadState('load');
    await expect(page).toHaveURL(/\/$/);
    expect(await bootAt(page), 'back/forward is a full local reload, never a cached snapshot').not.toBe(boot);
    assertClean();
  });

  test('a link to a page outside the shell (login) is a full document load', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    const boot = await bootAt(page);
    await page.evaluate(() => {
      const a = document.createElement('a');
      a.href = '/login'; a.id = 'probe-login'; a.textContent = 'x';
      document.querySelector('main')!.appendChild(a);
      (window as any).htmx.process(a);
    });
    await Promise.all([page.waitForEvent('load'), page.locator('#probe-login').click()]);
    // UT_AUTH=off in this project: /login sends the operator on to setup
    // or the sale screen — either way a document that is not this one.
    expect(await page.evaluate(() => (window as any).UT?.shellBootAt ?? 0)).not.toBe(boot);
    assertClean();
  });

  test('a boosted 404 becomes a full document load of the error page', async ({ page }) => {
    const assertClean = watchConsole(page, /404|Response Status Error Code/);
    await page.goto('/menu');
    const boot = await bootAt(page);
    await page.evaluate(() => {
      const a = document.createElement('a');
      a.href = '/no-such-page-2224'; a.id = 'probe-404'; a.textContent = 'x';
      document.querySelector('main')!.appendChild(a);
      (window as any).htmx.process(a);
    });
    await Promise.all([page.waitForEvent('load'), page.locator('#probe-404').click()]);
    await expect(page).toHaveURL(/no-such-page-2224/);
    expect(await page.evaluate(() => (window as any).UT?.shellBootAt ?? 0)).not.toBe(boot);
    assertClean();
  });

  test('a boosted HTML error page (RenderError 404) swaps in place with the rail intact', async ({ page }) => {
    const assertClean = watchConsole(page, /404|Response Status Error Code/);
    await page.goto('/menu');
    const boot = await bootAt(page);
    await page.evaluate(() => {
      const a = document.createElement('a');
      a.href = '/kitchen-display/no-such-station'; a.id = 'probe-html-404'; a.textContent = 'x';
      document.querySelector('main')!.appendChild(a);
      (window as any).htmx.process(a);
    });
    // The server must address the error page's own region (a status written
    // before the Content-Type is known — RenderError's shape).
    const err = await page.request.get('/kitchen-display/no-such-station', { headers: { 'HX-Request': 'true', 'HX-Boosted': 'true', 'UT-Shell-Nav': '1' } });
    expect(err.status()).toBe(404);
    expect(err.headers()['hx-retarget']).toBe('#ut-page');
    const theirs = /name="ut-shell" content="([^"]*)"/.exec(await err.text())?.[1];
    const mine = await page.evaluate(() => document.querySelector('meta[name="ut-shell"]')!.getAttribute('content'));

    await page.locator('#probe-html-404').click();
    await expect(page).toHaveURL(/no-such-station/);
    if (theirs === mine) {
      // Same shell: the error swaps in place — never htmx's default
      // body-innerHTML swap on error (which would destroy the on-screen
      // keyboard and duplicate the bug-report panel).
      expect(await bootAt(page)).toBe(boot);
    } else {
      // RenderError renders without the shop theme, so on a themed till the
      // error page is a different shell and loads as a full document.
      await page.waitForLoadState('load');
      expect(await page.evaluate(() => (window as any).UT?.shellBootAt ?? 0)).not.toBe(boot);
    }
    await expect(page.locator('#ut-page .nav')).toBeVisible();
    expect(await page.locator('#bugreport-panel').count()).toBe(1);
    assertClean();
  });

  test('an export link is a download, not a swap; plain forms are never boosted', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/audit');
    const boot = await bootAt(page);
    const download = page.waitForEvent('download');
    await page.locator('a[href^="/api/audit/export"]').first().click();
    await download;
    await expect(page).toHaveURL(/\/audit/);
    expect(await bootAt(page)).toBe(boot);

    // A plain link INSIDE a form (the audit "clear filters" link) is opted
    // back into boosting AND is still a shell navigation: same document,
    // shell header sent, region swapped — not htmx's default body swap.
    await page.goto('/audit?action=probe');
    const boot2 = await bootAt(page);
    const seen: string[] = [];
    page.on('request', (r) => { if (r.url().endsWith('/audit')) seen.push(r.headers()['ut-shell-nav'] ?? '-'); });
    await page.locator('#ut-page form a[href="/audit"]').click();
    await expect(page).toHaveURL(/\/audit$/);
    await expect(page.locator('#ut-page form a[href="/audit"]')).toHaveCount(0);
    expect(await bootAt(page)).toBe(boot2);
    expect(seen).toEqual(['1']);

    await page.goto('/settings');
    const boosted = await page.evaluate(() =>
      Array.from(document.querySelectorAll('#ut-page form')).filter((f) => f.getAttribute('hx-boost') !== 'false').length
    );
    expect(await page.locator('#ut-page form').count()).toBeGreaterThan(0);
    expect(boosted, 'every plain form must carry hx-boost="false"').toBe(0);
    assertClean();
  });

  test('versioned assets are immutable, HTML is not', async ({ page }) => {
    const css = await page.request.get('/public/app.css?v=probe');
    expect(css.headers()['cache-control']).toBe('public, max-age=31536000, immutable');
    const plain = await page.request.get('/public/app.css');
    expect(plain.headers()['cache-control']).toBe('no-cache');
    const html = await page.request.get('/menu');
    expect(html.headers()['cache-control'] ?? '').not.toContain('immutable');
  });
});
