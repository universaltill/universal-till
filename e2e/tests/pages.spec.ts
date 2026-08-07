import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// Every operator page renders without JS errors and shows its content —
// the smoke layer for htmx/Alpine wiring.
for (const [path, marker] of [
  ['/inventory', 'Days left'],
  // Not "Slow sellers" any more: since ut-docs#401 the heavy reports run on
  // demand behind tabs, so only the always-visible monitoring KPI row is on
  // the page itself. The tab swap gets its own test below.
  ['/reports', 'Revenue'],
  ['/catalog', 'Catalog'],
  ['/settings', 'System Settings'],
  ['/help', 'Alerts & notifications'],
] as const) {
  test(`page ${path} renders (${marker})`, async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto(path);
    await expect(page.locator('body')).toContainText(marker);
    assertClean();
  });
}

// ut-docs#401: /reports used to run ~16 queries on every page load. The heavy
// reports now sit behind tabs that fetch only on click. Go tests pin the
// handler split; this pins the half they cannot see — that a real browser
// loads the page WITHOUT the report in it, and that clicking the tab actually
// swaps the fragment in (the htmx wiring).
test('a reports tab runs its report only when clicked', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/reports');

  const panel = page.locator('#report-tab-panel');
  await expect(panel).toBeAttached();
  await expect(page.locator('body')).not.toContainText('Slow sellers');

  await page.locator('.report-tabs button', { hasText: 'Items' }).click();
  await expect(panel).toContainText('Slow sellers');

  assertClean();
});
