import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1294: the main till's own Tills row shows its discovery id as
// "Till ID: <uuid>" — the same label and string the LAN scan prints for it,
// so an operator can match the device by eye. A real mDNS scan can't run in
// e2e (UT_LAN_DISCOVERY=0), so the scan response is stubbed with the id the
// roster itself shows: the assertion is that both surfaces print it alike.

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
  test(`tills: main till row shows the scan's Till ID at ${vp.width}x${vp.height}`, async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize(vp);
    await page.goto('/tills');

    const line = page.getByTestId('tills-main-discovery-id');
    await expect(line).toHaveCount(1);
    const id = (await line.locator('bdi').textContent()) ?? '';
    expect(id).toMatch(UUID);
    await expect(line.locator('bdi')).toHaveAttribute('dir', 'ltr');
    await expect(line).toHaveText(`Till ID: ${id}`);

    // The line stays inside the name cell and the page never scrolls sideways
    // (the 36-char id wraps on a phone instead of widening the column).
    const inside = await line.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const td = el.closest('td')!.getBoundingClientRect();
      return r.left >= td.left - 0.5 && r.right <= td.right + 0.5;
    });
    expect(inside, 'Till ID line spills out of its cell').toBe(true);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, 'page scrolls sideways').toBeLessThanOrEqual(0);

    // The scan prints the same label and string for this main till.
    await page.route('**/api/sync/discover-primaries', (route) =>
      route.fulfill({ json: { data: { primaries: [{ name: 'Front', till_id: id, base_url: 'http://192.168.1.42:8080' }] }, error: null } }),
    );
    await page.locator('#discover-btn').click();
    const item = page.locator('#discover-results li').first();
    await expect(item).toContainText(`Till ID: ${id}`);
    await expect(item.locator('bdi')).toHaveText(id);

    // The 10 s roster poll re-renders the same value (minted once, then read).
    await page.waitForResponse((r) => r.url().includes('/ui/tills/roster'), { timeout: 15_000 });
    await expect(page.getByTestId('tills-main-discovery-id').locator('bdi')).toHaveText(id);

    assertClean();
  });
}
