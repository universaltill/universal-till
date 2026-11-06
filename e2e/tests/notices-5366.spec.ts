import { test, expect } from './fixtures';
import { watchConsole, gotoSettled } from './helpers';

// ut-docs#5366 (ADR-0176): notices from Universal Till on the till. Drives a
// REAL till (run-till-notices.sh) that pulls notices from a fake cloud
// (fake-notices-cloud.mjs, the ADR-0176 §4 wire contract) through the real
// cloudsync path: request/response, re-validation, the local mirror, acks.
//
// What this proves, in the order a shop sees it:
//   - flag off: no bell, no inbox;
//   - flag on: the bell badge counts what arrived within a check-in, the inbox
//     shows the text, opening a notice marks it read and the cloud is told;
//   - an "important" notice pops up once in flow on the sale screen, but never
//     while the payment panel is open (it waits, then shows);
//   - a "critical" notice that arrives MID-SALE appears as a strip and the
//     sale still completes: nothing blocks the tender;
//   - a manager acknowledges the strip and the cloud is told.
//
// Runs in the `notices` project (own till on 8103, fake cloud on 8104,
// check-ins every second via UT_TEST_CLOUDSYNC_TICK_MS), workers: 1.

const CLOUD = 'http://127.0.0.1:8104';

const ID_INFO = '5366a000-0000-4000-8000-000000000001';
const ID_IMPORTANT = '5366a000-0000-4000-8000-000000000002';
const ID_CRITICAL = '5366a000-0000-4000-8000-000000000003';
const ID_LATE = '5366a000-0000-4000-8000-000000000004';

function item(id: string, severity: string, title: string, body: string) {
  const now = Date.now();
  return {
    id, rev: 1, severity,
    sender: { kind: 'platform', name: 'Universal Till' },
    texts: { 'en-gb': { title, body } },
    starts_at: new Date(now - 3_600_000).toISOString(),
    expires_at: new Date(now + 48 * 3_600_000).toISOString(),
    created_at: new Date(now - 60_000).toISOString(),
  };
}

const INFO = item(ID_INFO, 'info', 'New: split a bill by seat', 'Version 1.42 splits a **table** bill by seat.');
const IMPORTANT = item(ID_IMPORTANT, 'important', 'Training booked for Tuesday', 'Your two-hour session starts at 10:00.');
const CRITICAL = item(ID_CRITICAL, 'critical', 'Update the till before Friday', 'A security fix is available for this till.');

async function setNotices(request: import('@playwright/test').APIRequestContext, items: unknown[]) {
  const r = await request.post(`${CLOUD}/__notices`, { data: items });
  expect(r.ok()).toBe(true);
}
async function cloudAcks(request: import('@playwright/test').APIRequestContext) {
  return (await (await request.get(`${CLOUD}/__acks`)).json()) as { acks: Record<string, string>[]; asks: number };
}

test.describe.configure({ mode: 'serial' });

test.describe('notices on the till (ut-docs#5366)', () => {
  test.setTimeout(120_000);

  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('flag off: no bell, no inbox; flag on: the bell, a badge and the inbox', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await setNotices(request, [INFO, IMPORTANT]);

    // Off by default: nothing in the rail, and the route does not exist.
    await page.goto('/');
    await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
    await expect(page.locator('[data-testid="notices-bell"]')).toHaveCount(0);
    expect((await page.request.get('/notices')).status()).toBe(404);
    expect((await cloudAcks(request)).asks, 'flag off: the till asks the cloud for no notices').toBe(0);

    // Turn it on in Settings (the real card).
    await page.goto('/settings#settings-notices');
    const box = page.locator('[data-testid="notices-enabled"]');
    await box.check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/notices') && r.status() === 204),
      page.locator('#settings-notices button[type=submit]').click(),
    ]);

    // Within a check-in the badge counts what the cloud listed (2 unread).
    await expect
      .poll(async () => {
        await gotoSettled(page, '/'); // the Settings save reloads its page: let it finish first
        return (await page.locator('[data-testid="notices-badge"]').textContent({ timeout: 2000 }).catch(() => '')) ?? '';
      }, { timeout: 30_000, intervals: [1000] })
      .toBe('2');
    const bell = page.locator('[data-testid="notices-bell"]');
    await expect(bell).toHaveAttribute('aria-label', 'Notices, 2 unread');
    // The bell sits in the rail's status group, not among the navigation items.
    expect(await page.locator('.nav-right [data-testid="notices-bell"]').count()).toBe(1);

    await bell.click();
    await expect(page).toHaveURL(/\/notices/);
    await expect(page.locator('[data-testid="notice-row"]')).toHaveCount(2);
    await expect(page.locator('main')).toContainText('Training booked for Tuesday');
    await expect(page.locator('[data-testid="notice-sender"]').first()).toContainText('Universal Till');

    // Opening marks read: the rendered body (bold) is shown, the badge drops.
    await page.locator('[data-notice-id="' + ID_INFO + '"] .notice-hd').click();
    await expect(page.locator('#notice-body-' + ID_INFO + ' strong')).toHaveText('table');
    await expect(page.locator('[data-testid="notices-badge"]')).toHaveText('1');
    await page.locator('[data-notice-id="' + ID_IMPORTANT + '"] .notice-hd').click();
    await expect(page.locator('[data-testid="notices-badge"]')).toHaveCount(0);

    // The cloud is told (read_at per id) on a later check-in.
    await expect
      .poll(async () => (await cloudAcks(request)).acks.filter((a) => a.read_at).map((a) => a.id).sort(), { timeout: 20_000 })
      .toEqual([ID_INFO, ID_IMPORTANT].sort());
    assertClean();
  });

  test('a critical notice arriving mid-sale shows as a strip and the sale still completes', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container .products');
    await expect(page.locator('[data-testid="notices-strip"]')).toHaveCount(0);

    // A sale in progress.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // The critical notice arrives while the basket is open. No reload.
    await setNotices(request, [INFO, IMPORTANT, CRITICAL]);
    const strip = page.locator('[data-testid="notices-strip"]');
    await expect(strip).toBeVisible({ timeout: 40_000 });
    await expect(strip).toContainText('Critical: Update the till before Friday');
    await expect(page.locator('#basket')).toContainText('Coca-Cola'); // basket untouched
    // In flow, not an overlay: it takes layout height above the main region.
    const box = await strip.boundingBox();
    expect(box && box.height).toBeGreaterThan(20);
    await page.screenshot({ path: test.info().outputPath('critical-strip-mid-sale.png') });

    // Tender completes, unhindered.
    await page.locator('[data-testid="payment-open"]').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender') && r.ok()),
      page.locator('[data-testid="pay-default"]').first().click(),
    ]);
    // The receipt view replaces the basket: the sale completed.
    await expect(page.locator('#basket')).toContainText('Auto-reset in 60s', { timeout: 15_000 });
    await expect(strip).toBeVisible(); // the sale did not dismiss it

    // A manager acknowledges: the strip collapses and the cloud is told.
    await page.locator('[data-testid="notices-strip-ack"]').click();
    await expect(strip).toHaveCount(0);
    await expect
      .poll(async () => (await cloudAcks(request)).acks.some((a) => a.id === ID_CRITICAL && a.acked_at), { timeout: 20_000 })
      .toBe(true);
    assertClean();
  });

  test('an important toast waits while the payment panel is open, then shows once', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container .products');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);

    await page.locator('[data-testid="payment-open"]').click();
    await expect(page.locator('#payment-overlay')).toHaveJSProperty('open', true);

    // Arrives while paying; wait until the till has really pulled it.
    const before = (await cloudAcks(request)).asks;
    await setNotices(request, [item(ID_LATE, 'important', 'Webinar tomorrow', 'Importing your price list.')]);
    await expect.poll(async () => (await cloudAcks(request)).asks, { timeout: 20_000 }).toBeGreaterThan(before + 2);
    // The toast poll runs every 15 s: wait out more than one period.
    await page.waitForTimeout(17_000);
    await expect(page.locator('[data-testid="notices-toast"]')).toHaveCount(0);

    // Back at the basket: it shows, in flow, at the top of the product grid.
    await page.locator('[data-testid="payment-close"]').click();
    const toast = page.locator('[data-testid="notices-toast"]');
    await expect(toast).toBeVisible({ timeout: 10_000 });
    await expect(toast).toContainText('Webinar tomorrow');
    await expect(page.locator('.products [data-testid="notices-toast"]')).toHaveCount(1);
    await page.screenshot({ path: test.info().outputPath('important-toast.png') });
    await expect(toast).toHaveCount(0, { timeout: 20_000 }); // gone after ~10 s
    await page.request.post('/api/pos/reset');
    assertClean();
  });

  test('hostile items never land; an expired or withdrawn notice disappears on its own', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    const ID_HTML = '5366a000-0000-4000-8000-00000000000a';
    const ID_BIDI = '5366a000-0000-4000-8000-00000000000b';
    const ID_SHORT = '5366a000-0000-4000-8000-00000000000c';
    const ID_GOOD = '5366a000-0000-4000-8000-00000000000d';
    const html = item(ID_HTML, 'critical', '<script>window.__pwned=1</script>', '[x](javascript:alert(1))');
    const bidi = item(ID_BIDI, 'important', 'Update ‮gnp.exe', 'see ​this');
    const short = item(ID_SHORT, 'info', 'Gone in seconds', 'Expires almost at once.');
    short.expires_at = new Date(Date.now() + 8_000).toISOString();
    const good = item(ID_GOOD, 'info', 'Stays until withdrawn', 'A plain notice.');
    await setNotices(request, [html, bidi, short, good]);

    // Only the two valid items count; the hostile ones were dropped on pull.
    const badge = async () => {
      await gotoSettled(page, '/');
      return (await page.locator('[data-testid="notices-badge"]').textContent({ timeout: 2000 }).catch(() => '')) ?? '';
    };
    await expect.poll(badge, { timeout: 30_000, intervals: [1000] }).toBe('2');
    await expect(page.locator('[data-testid="notices-strip"]')).toHaveCount(0); // the hostile "critical" never stripped
    await page.goto('/notices?tab=all');
    await expect(page.locator('[data-testid="notice-row"]')).toHaveCount(2);
    await expect(page.locator(`[data-notice-id="${ID_HTML}"]`)).toHaveCount(0);
    await expect(page.locator(`[data-notice-id="${ID_BIDI}"]`)).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBeUndefined();

    // Expiry: the till erases the row itself (no cloud change needed).
    await expect.poll(badge, { timeout: 40_000, intervals: [1000] }).toBe('1');
    await page.goto('/notices?tab=all');
    await expect(page.locator(`[data-notice-id="${ID_SHORT}"]`)).toHaveCount(0);
    await expect(page.locator(`[data-notice-id="${ID_GOOD}"]`)).toHaveCount(1);

    // Withdrawn (killed) on the cloud: the mirror drops it at the next pull.
    await setNotices(request, []);
    await expect.poll(badge, { timeout: 30_000, intervals: [1000] }).toBe('');
    await page.goto('/notices?tab=all');
    await expect(page.locator('[data-testid="notices-empty"]')).toBeVisible();
    assertClean();
  });
});
