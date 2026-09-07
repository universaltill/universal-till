import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1556 review follow-up: both LAN printer-discovery scripts — the
// original one on /kitchen-stations and the copy #1556 added to
// Settings → Printer — went straight from fetch() to r.json() with no
// status check. A non-2xx response (403 when a cashier reaches the
// endpoint, 500 when the scan itself fails) still parses as JSON and
// simply has no `data.printers`, so `(j.data && j.data.printers) || []`
// produced an empty list and the UI announced the reassuring "no printers
// found on this network" — sending the operator to hunt a cable or a
// firewall for a fault that is actually on the server. The two messages
// are deliberately different strings, so asserting the error text here
// distinguishes "failed" from "found nothing" and would fail on the old
// code, which showed the latter.
const PAGES = [
  {
    name: 'Settings → Printer',
    url: '/settings',
    button: '#printer-discover-btn',
    message: '#printer-discover-msg',
    // settings.printer.discover.error
    error: 'Could not search this network for printers.',
    // settings.printer.discover.none_found — what the old code showed
    noneFound: 'No AppSocket/JetDirect printers answered on this network.',
  },
  {
    name: 'Kitchen stations',
    url: '/kitchen-stations',
    button: '#discover-printers-btn',
    message: '#discover-printers-msg',
    // kitchenstations.discover.error
    error: 'Could not search this network for printers.',
    // kitchenstations.discover.none_found — what the old code showed
    noneFound: 'No printers found on this network.',
  },
];

for (const page_ of PAGES) {
  test(`${page_.name}: a failed discovery scan says it failed, not "none found"`, async ({ page }) => {
    // Chromium logs its own "Failed to load resource: … 500" for the
    // stubbed response no matter how the page handles it, so exempt exactly
    // that line (helpers.ts's extraExempt, ut-docs#916) — every other
    // console error still fails this test, including the one the old code
    // would have produced if r.json() had thrown on a non-JSON body.
    const assertClean = watchConsole(page, /^Failed to load resource:.*500/);

    // The failure is server-side, so stub the endpoint rather than trying
    // to break a real mDNS/port-9100 scan. The body is deliberately
    // well-formed JSON: the point is that a valid JSON error body used to
    // be indistinguishable from an empty result.
    await page.route('**/api/kitchen-stations/discover-printers', async (route) => {
      await route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'scan failed' }),
      });
    });

    await page.goto(page_.url);

    const btn = page.locator(page_.button);
    const msg = page.locator(page_.message);
    await expect(btn).toBeEnabled();

    await btn.click();
    await expect(msg).toHaveText(page_.error);
    await expect(msg).not.toHaveText(page_.noneFound);

    // The .catch() also has to hand the button back, or the operator
    // cannot retry once the server recovers.
    await expect(btn).toBeEnabled();

    assertClean();
  });
}
