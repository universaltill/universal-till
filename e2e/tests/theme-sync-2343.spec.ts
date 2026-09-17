import { test, expect } from './fixtures';

// ut-docs#2343: a theme changed via cloud.universaltill.com's set_setting
// directive (ADR-0018) lands in the till's live state the moment it's
// applied -- the same POST /api/settings/theme -> SaveState/SetState path
// the Settings page's own form uses (rederive on the cloud path, direct
// SetState on the local one) -- so this test doesn't need a real cloud
// round-trip to prove the fix: any theme change landing in the background
// while a page sits open is the same shape, whichever actor made it. It
// plays that actor with a plain server-to-server POST (no browser UI
// involved, same as the cloud directive path really is) and proves an
// already-open, never-reloaded page picks the change up live.
//
// A server-string-assertion test (theme_sync_test.go) cannot catch what
// this one exists for -- two real bugs only a live browser surfaced, both
// found and fixed while writing this spec:
// 1. An OOB fragment that is a bare <link> (an earlier draft of this fix)
//    is silently dropped by htmx's response parser -- DOMParser hoists a
//    head-only element into a throwaway <head>, leaving <body> (all
//    handleOutOfBandSwaps ever scans) empty.
// 2. The listener that applies the swap read `e.detail.target.dataset`
//    directly; on the page's own very first "load"-triggered poll,
//    e.detail.target is the PRE-swap element (no data-theme at all), so
//    every single page load looked like "theme changed to empty" and
//    wrongly cleared #theme-css. Fixed by re-reading the DOM fresh
//    (getElementById) instead of trusting the event's own target.
test('a theme changed elsewhere reaches an already-open page live, without a reload disturbing it', async ({
  page,
  browser,
}) => {
  // The long-open kiosk session: never navigates again after this, exactly
  // the case that has nobody to trigger settings.html's own
  // window.location.reload().
  const kiosk = await browser.newPage();
  await kiosk.emulateMedia({ reducedMotion: 'reduce' });
  await kiosk.goto('/');

  // Proof-of-no-reload marker: a reload would wipe this un-submitted input.
  await kiosk.locator('input[name="code"]').fill('unsaved-scan-marker');

  // Something else changes the theme -- a plain POST to the same endpoint
  // the Settings page's own form uses, no browser UI involved, standing in
  // for the cloud directive path (ADR-0018's SetSetting hook writes the
  // exact same "theme" key through the exact same handler; see
  // cloudsync_wire.go / internal/pages/settings_page.go's
  // "/api/settings/theme").
  await page.goto('/');
  const status = await page.evaluate(async () => {
    const r = await fetch('/api/settings/theme', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'theme=slate',
    });
    return r.status;
  });
  expect(status).toBe(204);

  // The kiosk page has done nothing since -- no navigation, no reload.
  // Production polls every 30s; htmx.ajax replicates exactly what that
  // interval trigger fires, without the test waiting on a real 30s clock.
  const themeFetch = kiosk.waitForResponse((r) => r.url().endsWith('/themes/slate.css'));
  await kiosk.evaluate(() =>
    (window as unknown as { htmx: { ajax: (m: string, u: string, o: object) => void } }).htmx.ajax(
      'GET',
      '/ui/theme-sync',
      { target: '#theme-sync-poll', swap: 'none' },
    ),
  );
  await themeFetch;

  await expect(kiosk.locator('#theme-css')).toHaveAttribute('href', /\/themes\/slate\.css/);
  await expect(kiosk.locator('#theme-css')).toHaveAttribute('data-theme', 'slate');

  // Genuinely never reloaded: the unsaved input survived.
  await expect(kiosk.locator('input[name="code"]')).toHaveValue('unsaved-scan-marker');

  // A settled kiosk stops touching the stylesheet: forcing the SAME poll
  // again (theme unchanged now) must not re-fetch it -- an earlier draft
  // of this fix re-sent (and re-applied) the swap forever, because the
  // comparison lived in a query-string value baked in at render time
  // rather than in the client's own record of what #theme-css last showed.
  let refetched = false;
  kiosk.on('response', (r) => {
    if (r.url().endsWith('/themes/slate.css')) refetched = true;
  });
  await kiosk.evaluate(() =>
    (window as unknown as { htmx: { ajax: (m: string, u: string, o: object) => void } }).htmx.ajax(
      'GET',
      '/ui/theme-sync',
      { target: '#theme-sync-poll', swap: 'none' },
    ),
  );
  await kiosk.waitForTimeout(300);
  expect(refetched).toBe(false);

  await kiosk.close();
});

// The bug this catches (see the header comment above) is specific to the
// page's own FIRST "load"-triggered poll, which the test above never
// exercises directly (it forces a poll manually, after the page has
// already settled) -- this one asserts the initial render survives that
// very first automatic poll, with no manual poll involved at all.
test('a fresh page load keeps its own live theme through its own first automatic poll', async ({
  page,
}) => {
  // Just to establish an origin for the fetch below -- the real "fresh
  // load" under test is the page.goto('/') AFTER the theme change, not
  // this one.
  await page.goto('/');
  const status = await page.evaluate(async () => {
    const r = await fetch('/api/settings/theme', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'theme=amber',
    });
    return r.status;
  });
  expect(status).toBe(204);

  await page.goto('/');
  await expect(page.locator('#theme-css')).toHaveAttribute('data-theme', 'amber');
  await expect(page.locator('#theme-css')).toHaveAttribute('href', /\/themes\/amber\.css/);
});
