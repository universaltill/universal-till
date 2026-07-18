import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// Every operator page renders without JS errors and shows its content —
// the smoke layer for htmx/Alpine wiring.
for (const [path, marker] of [
  ['/inventory', 'Days left'],
  ['/reports', 'Slow sellers'],
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
