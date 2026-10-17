import { test, expect } from './fixtures';
import { watchConsole } from './helpers';
import * as http from 'http';

// ut-docs#4477 (ADR-0160 §6): Settings → Language lists the plugin catalog's
// language packs next to the installed languages. Picking one that isn't
// installed queues a background download, and the Language card shows a
// polling status chip (never a modal) until the pack has arrived.
//
// Drives the real path against an in-process fake ut-cloud that lists one
// German language pack (GET /v1/catalog/plugins) and refuses everything
// else, so the download itself never completes and the till never
// registers: the chip stays "Downloading Deutsch…". Removing the pending
// download under Settings → Data turns the chip into its "couldn't be
// downloaded" state. The switch once a pack HAS arrived is covered by the Go
// handler test (settings_language_install_test.go), which installs a real
// pack from a fake marketplace.
//
// Runs in the `diagnostics` project (run-till-diagnostics.sh points the till
// at 127.0.0.1:8096, auth off), the one project whose till talks to a fake
// cloud; workers: 1, so the port is never shared. The file name sorts it
// FIRST in that project on purpose: the other specs' fakes answer the catalog
// with an empty list, and the till caches a successful catalog answer for 5
// minutes (setupLanguageCatalogTTL), so after one of them has opened
// /settings this spec's German listing would not show for minutes.

const CLOUD_PORT = 8096;

function startLanguageCatalogCloud(): Promise<http.Server> {
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const url = new URL(req.url ?? '/', `http://127.0.0.1:${CLOUD_PORT}`);
      res.setHeader('Content-Type', 'application/json');
      if (url.pathname === '/v1/catalog/plugins') {
        res.writeHead(200);
        return res.end(JSON.stringify({
          plugins: [{
            id: 'listing-lang-de-4477', listing_id: 'listing-lang-de-4477',
            name: 'German language pack', version: '1.0.0', canonical_type: 'language',
            capabilities: ['language'], available_locales: ['de'], trust_tier: 'verified',
          }],
        }));
      }
      res.writeHead(503);
      res.end(JSON.stringify({ data: null, error: { code: 'unavailable', message: 'e2e' } }));
    });
  });
  return new Promise((resolve) => server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(server)));
}

test.describe.serial('Settings → Language installs a catalog language on select (ut-docs#4477)', () => {
  let cloud: http.Server;
  test.beforeAll(async () => {
    cloud = await startLanguageCatalogCloud();
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => cloud.close(() => r()));
  });

  test('pick a download language: chip while it downloads, then its unavailable state', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });

    const card = page.locator('#settings-language');
    const downloads = card.locator('[data-testid="settings-language-downloads"]');
    // GET /settings never waits on the catalog: the first render refreshes
    // it in the background and a later one lists it. (A repeated goto to the
    // same #hash URL is a same-document navigation, so reload.)
    await page.goto('/settings#settings-language');
    await expect(async () => {
      await page.reload();
      await expect(downloads).toHaveCount(1, { timeout: 1000 });
    }).toPass({ timeout: 20_000 });
    await expect(downloads.locator('option[value="de"]')).toHaveText('Deutsch');

    await card.locator('select[name="locale"]').selectOption('de');
    await Promise.all([
      page.waitForEvent('load'),
      card.getByRole('button', { name: 'Apply' }).click(),
    ]);

    // Still English: nothing switches before the pack has arrived.
    await expect(page.locator('html')).toHaveAttribute('lang', /^en/);
    const chip = page.locator('[data-testid="language-install-chip"]');
    await expect(chip).toHaveAttribute('data-state', 'installing');
    await expect(chip).toContainText('Downloading Deutsch');
    await expect(chip).toHaveAttribute('role', 'status');
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    await chip.scrollIntoViewIfNeeded();
    await page.screenshot({ path: test.info().outputPath('language-installing-1024x600.png') });

    // The chip fits its card at the phone floor too.
    await page.setViewportSize({ width: 360, height: 800 });
    await chip.scrollIntoViewIfNeeded();
    const fit = await chip.evaluate((el) => {
      const c = el.getBoundingClientRect();
      const p = (el.closest('.card') as HTMLElement).getBoundingClientRect();
      return { inside: c.left >= p.left - 1 && c.right <= p.right + 1, pageScrollsX: document.documentElement.scrollWidth > window.innerWidth };
    });
    expect(fit.inside, 'chip stays inside the Language card at 360 px').toBe(true);
    expect(fit.pageScrollsX, 'no sideways page scroll at 360 px').toBe(false);
    await card.screenshot({ path: test.info().outputPath('language-installing-360.png') });
    await page.setViewportSize({ width: 1024, height: 600 });

    // RTL: the same chip in Persian (this browser only; ?lang= sets a cookie).
    await page.goto('/settings?lang=fa#settings-language');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await expect(chip).toHaveAttribute('data-state', 'installing');
    await card.screenshot({ path: test.info().outputPath('language-installing-fa.png') });
    await page.goto('/settings?lang=en#settings-language');

    // The download is the ordinary pending base plugin under Settings → Data;
    // once it is removed there, the Language card's chip says, on its next
    // poll, that it couldn't be downloaded, and stops polling. (A hash-only
    // goto is a same-document navigation: it switches the Settings pane
    // without reloading the page.)
    await page.goto('/settings#settings-data');
    const pendingRow = page.locator('[data-testid="pending-base-plugin"]#pending-plugin-language');
    await expect(pendingRow).toHaveCount(1);
    await pendingRow.locator('[data-testid="pending-base-plugin-dismiss"]').click();
    await expect(pendingRow).toHaveCount(0);
    await page.goto('/settings#settings-language');
    await expect(chip).toHaveAttribute('data-state', 'unavailable');
    await expect(chip).toContainText("Deutsch couldn't be downloaded");
    await expect(chip).not.toHaveAttribute('hx-trigger', /.+/);

    // Shown once: a fresh page has no chip.
    await page.reload();
    await expect(chip).toHaveCount(0);
    await expect(page.locator('html')).toHaveAttribute('lang', /^en/);
    assertClean();
  });
});
