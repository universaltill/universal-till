import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3218: on iOS, discovery goes through Bonjour, and the user can
// refuse the Local Network permission. The server then answers 403 with
// error.code "local_network_denied" (internal/pages/discovery_api.go). The
// page must say what happened and how to fix it — not the generic "could
// not search", and not "no primaries found" (which is what reading the
// JSON's null data would show).
const denied = {
  status: 403,
  json: { data: null, error: { code: 'local_network_denied', message: 'local network access denied' } },
};

test('Tills page explains a refused Local Network permission', async ({ page }) => {
  // Chromium logs the mocked 403 itself as a failed resource load.
  const assertClean = watchConsole(page, /^Failed to load resource:.*403/);
  await page.route('**/api/sync/discover-primaries', (route) => route.fulfill(denied));

  await page.setViewportSize({ width: 390, height: 844 }); // a phone, where this happens
  await page.goto('/tills');
  const btn = page.locator('#discover-btn');
  await btn.click();

  const msg = page.locator('#discover-msg');
  await expect(msg).toContainText('Local Network');
  await expect(msg).toContainText('pairing code');
  await expect(msg).not.toContainText('Could not search');
  await expect(msg).not.toContainText('No primaries found');
  await expect(btn).toBeEnabled(); // the operator can try again after allowing it
  await expect(page.locator('#discover-results li')).toHaveCount(0);

  // The long message wraps inside the phone width instead of pushing the
  // page sideways.
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);

  assertClean();
});

// The first-boot wizard's "Join an existing shop" scan has the same branch
// (setup.html); an iPhone set up as a second till meets it there first.
test('setup wizard explains a refused Local Network permission', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*403/);
  await page.route('**/api/setup/discover-primaries', (route) => route.fulfill(denied));

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/setup');
  const btn = page.locator('#setup-discover-btn');
  if (!(await btn.isVisible())) {
    // The scan lives behind the wizard's "Join an existing shop" choice.
    await page.getByText(/Join an existing shop/i).first().click();
  }
  await btn.click();

  const msg = page.locator('#setup-discover-msg');
  await expect(msg).toContainText('Local Network');
  await expect(msg).not.toContainText('No primaries found');
  await expect(btn).toBeEnabled();

  assertClean();
});
