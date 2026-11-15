import { test, expect } from './fixtures';
import { ADMIN_PIN, ensureOperator } from './helpers';

// ut-docs#5156 (security): the step-up PIN check (checkStepUp, ADR-0087)
// refuses a request that is not a same-origin call from the till's own
// page, before any PIN is looked up. Drives a REAL manager session on the
// auth till. The probe is the archive purge route with an archive id that
// does not exist: once the PIN is accepted the handler answers 404 (nothing
// to purge, nothing destroyed); a refused request gets the PIN prompt back
// instead.
//
// Needs the `auth` project (a real session). Named to sort after
// login.spec.ts, which needs the shared auth till still first-boot.

const PROBE = '/api/data/reset-archives/no-such-batch-5156/purge';

test.describe('Step-up PIN refuses cross-origin requests (ut-docs#5156)', () => {
  test('same-origin page passes the PIN check; cross-site and foreign Origin are refused', async ({ page }) => {
    await ensureOperator(page); // admin — first-boot wizard or PIN re-login
    await page.goto('/settings');

    // Control: a fetch from the till's own page (the browser sets
    // Sec-Fetch-Site: same-origin and its own Origin) gets past the PIN
    // check to the handler, which finds no such archive.
    const sameOrigin = await page.evaluate(async ({ url, pin }) => {
      const r = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ override_pin: pin }).toString(),
      });
      return { status: r.status, prompt: r.headers.get('X-UT-Response') };
    }, { url: PROBE, pin: ADMIN_PIN });
    expect(sameOrigin.prompt, 'same-origin request was not sent back to the PIN prompt').toBeNull();
    expect(sameOrigin.status, 'same-origin request reached the handler (no such archive)').toBe(404);

    // The same session and the same valid PIN, sent as a browser would send
    // it from another site: refused with the prompt, PIN never checked.
    for (const headers of [
      { 'Sec-Fetch-Site': 'cross-site' },
      { 'Sec-Fetch-Site': 'same-site' },
      { Origin: 'http://evil.example' },
    ]) {
      const resp = await page.request.post(PROBE, { form: { override_pin: ADMIN_PIN }, headers });
      const label = JSON.stringify(headers);
      expect(resp.status(), `${label}: answered with the prompt`).toBe(200);
      expect(resp.headers()['x-ut-response'], `${label}: PIN prompt, not the handler`).toBe('elevation-prompt');
      expect(await resp.text(), `${label}: says approval isn't available`).toContain('available on this device right now');
    }
  });
});
