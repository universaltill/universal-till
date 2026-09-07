import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#76: the in-POS Bluetooth pairing panel is reachable under
// UT_AUTH=off (canPerform's dev/CI bypass, same as every sibling admin page
// in admin-pages-auth-off-902.spec.ts) and renders its full layout on a
// host with NO Bluetooth — the e2e runner has no bluetoothd, a developer's
// Mac has no system bus at all, and a Linux dev box may have a live adapter.
// So this asserts only what is identical across all three: the page (not a
// bare 403/500 body), the scan button (rendered in every state, merely
// disabled when Bluetooth is unavailable), and a clean console. Real
// pairing needs real hardware — a documented Tester-side gap, not something
// this spec can fake.
test.describe('Bluetooth devices panel (ut-docs#76)', () => {
  test('/bluetooth-devices renders without Bluetooth on the host', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/bluetooth-devices');
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('[data-testid=bt-paired-list]')).toBeVisible();
    await expect(page.locator('#bt-scan-btn')).toBeAttached();
    assertClean();
  });
});
