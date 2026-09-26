import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2788: the tablet still "refreshes" now and then and nobody can say
// why. Every whole-page load the till's own code causes now names its reason
// (UT.reload / UT.noteNav / an HX-Refresh or HX-Redirect response), and the
// NEXT document reports it to POST /api/diag/reload-reason, which writes one
// "page reload: reason=…" line to the till log. A reload nobody announced
// (native WebView reload, pull-to-refresh, F5) reports "unattributed". An
// ordinary navigation reports nothing -- the log must stay quiet unless a
// page really reloaded.

type Page = import('@playwright/test').Page;
type Report = { reason: string; path: string; nav_type: string; status: number };

// Chromium never exposes a sendBeacon body to the protocol (postData() and
// postDataBuffer() are both empty), so every document wraps sendBeacon and
// keeps the reported bodies; the response listener proves the server took it.
async function collectReports(page: Page): Promise<() => Promise<Report[]>> {
  await page.addInitScript(() => {
    const w = window as unknown as { __ut2788: string[] };
    w.__ut2788 = [];
    const orig = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = (url: string | URL, data?: BodyInit | null) => {
      if (String(url).includes('/api/diag/reload-reason') && data instanceof Blob) {
        data.text().then((t) => w.__ut2788.push(t));
      }
      return orig(url, data);
    };
  });
  const statuses: number[] = [];
  page.on('response', (r) => { if (r.url().includes('/api/diag/reload-reason')) statuses.push(r.status()); });
  return async () => {
    const bodies = await page.evaluate(() => (window as unknown as { __ut2788?: string[] }).__ut2788 ?? []);
    return bodies.map((b, i) => ({ ...JSON.parse(b), status: statuses[i] }));
  };
}

test.describe('whole-page reloads name their reason (ut-docs#2788)', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
  });

  test('an ordinary load and a boosted navigation report nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const reports = await collectReports(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container .products .btn-tile');
    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await page.waitForTimeout(500);
    expect(await reports()).toEqual([]);
    assertClean();
  });

  test('an unannounced reload (F5, native WebView reload) reports "unattributed"', async ({ page }) => {
    const assertClean = watchConsole(page);
    const reports = await collectReports(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container .products .btn-tile');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/diag/reload-reason')),
      page.reload(),
    ]);
    await expect.poll(async () => (await reports()).length).toBe(1);
    expect((await reports())[0]).toMatchObject({ reason: 'unattributed', path: '/', nav_type: 'reload', status: 204 });
    assertClean();
  });

  test('UT.reload(reason) reports that reason after the reload, once', async ({ page }) => {
    const assertClean = watchConsole(page);
    const reports = await collectReports(page);
    await page.goto('/menu');
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/diag/reload-reason')),
      page.evaluate(() => (window as unknown as { UT: { reload: (r: string) => void } }).UT.reload('e2e-Check 2788!')),
    ]);
    await expect.poll(async () => (await reports()).length).toBe(1);
    // Lower-cased and sanitised to the server's accepted charset.
    expect((await reports())[0]).toMatchObject({ reason: 'e2e-check 2788_', path: '/menu', nav_type: 'reload', status: 204 });
    // The stash is consumed: a later plain navigation reports nothing more.
    await page.goto('/');
    await page.waitForSelector('.pos-container .products .btn-tile');
    await page.waitForTimeout(500);
    // A new document: its own beacon log starts empty and must stay so.
    expect(await reports()).toEqual([]);
    assertClean();
  });
});
