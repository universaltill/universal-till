import { test, expect, type Page } from './fixtures';

// ut-docs#3095: the status-bar light (#sb-conn) was navigator.onLine only,
// so on Wi-Fi whose router has no internet it still said "Online". The
// shell now also polls GET /ui/net-status (the till's own probe of its cloud
// host) and shows "No internet" in the offline style when the network is up
// but cloud_reachable is false. true / null (unknown — the e2e till's
// loopback endpoint disables the probe) leave today's behaviour alone.

async function routeNetStatus(page: Page, value: boolean | null) {
  await page.route('**/ui/net-status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { cloud_reachable: value }, error: null }),
    }),
  );
}

const light = (page: Page) => page.locator('[data-testid=status-indicator]');

test.describe('cloud reachability light (ut-docs#3095)', () => {
  test('the real endpoint answers null on a loopback-configured till', async ({ page }) => {
    const res = await page.request.get('/ui/net-status');
    expect(res.status()).toBe(200);
    expect(await res.json()).toEqual({ data: { cloud_reachable: null }, error: null });
  });

  test('cloud unreachable while online shows No internet, offline style', async ({ page }) => {
    await routeNetStatus(page, false);
    await page.goto('/');
    await expect(light(page)).toHaveClass(/is-offline/);
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
  });

  for (const value of [true, null]) {
    test(`cloud_reachable ${value} shows Online`, async ({ page }) => {
      let polled = false;
      await page.route('**/ui/net-status', (route) => {
        polled = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: { cloud_reachable: value }, error: null }),
        });
      });
      await page.goto('/');
      await expect.poll(() => polled).toBe(true);
      await expect(light(page)).not.toHaveClass(/is-offline/);
      await expect(light(page).locator('.sb-conn-text')).toHaveText('Online');
    });
  }

  test('the device network being down still wins: Offline', async ({ page, context }) => {
    await routeNetStatus(page, false);
    await page.goto('/');
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
    await context.setOffline(true);
    await expect(light(page).locator('.sb-conn-text')).toHaveText('Offline');
    await expect(light(page)).toHaveClass(/is-offline/);
    await context.setOffline(false);
  });

  test('a failed poll keeps the last answer', async ({ page }) => {
    let fail = false;
    await page.route('**/ui/net-status', (route) =>
      fail
        ? route.abort()
        : route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ data: { cloud_reachable: false }, error: null }),
          }),
    );
    await page.goto('/');
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
    fail = true;
    // The online event re-polls immediately; the aborted fetch must not
    // flip the light back to Online.
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await page.waitForTimeout(300);
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
  });
  test('boosted navigations do not stack pollers, and keep the answer', async ({ page }) => {
    // Review blocker: the status-bar script re-runs on every boosted
    // navigation (#ut-page is swapped); a bare setInterval stacked one more
    // 10 s poller per visit. One interval must survive, and the answer
    // (window.__utNet) must carry over instead of flashing "Online".
    await page.clock.install();
    let polls = 0;
    await page.route('**/ui/net-status', (route) => {
      polls++;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { cloud_reachable: false }, error: null }),
      });
    });
    await page.goto('/');
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
    const boot = await page.evaluate(() => (window as any).UT.shellBootAt as number);
    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    expect(await page.evaluate(() => (window as any).UT.shellBootAt as number)).toBe(boot);
    await expect(light(page).locator('.sb-conn-text')).toHaveText('No internet');
    polls = 0;
    await page.clock.runFor(30_000);
    // One poller: 3 ticks in 30 s (a stacked second one would make 6).
    expect(polls).toBeLessThanOrEqual(4);
    expect(polls).toBeGreaterThanOrEqual(2);
  });
});
