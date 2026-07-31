import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#155: touch tills must not show a mouse cursor by default —
// cursor.js hides it on touch-capable devices and brings it back the
// moment a REAL mouse moves (a kiosk Pi with a plugged-in mouse must
// stay usable). Non-touch desktops are untouched.

test.describe('touch till', () => {
  test.use({ hasTouch: true });

  test('cursor is hidden by default and returns when a mouse moves', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    await expect(page.locator('html')).toHaveClass(/cursor-hidden/);
    const cursor = await page.evaluate(() => getComputedStyle(document.body).cursor);
    expect(cursor).toBe('none');

    // A real mouse movement restores the cursor…
    await page.mouse.move(200, 200);
    await page.mouse.move(220, 220);
    await expect(page.locator('html')).not.toHaveClass(/cursor-hidden/);

    // …and the next touch hides it again.
    await page.touchscreen.tap(400, 300);
    await expect(page.locator('html')).toHaveClass(/cursor-hidden/);
    assertClean();
  });
});

test.describe('non-touch desktop', () => {
  test('cursor is never hidden', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await expect(page.locator('html')).not.toHaveClass(/cursor-hidden/);
    assertClean();
  });
});
