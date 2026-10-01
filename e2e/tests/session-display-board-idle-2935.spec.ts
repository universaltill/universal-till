import { test, expect } from '@playwright/test';
import { ensureOperator } from './helpers';

// ut-docs#2223: exempt from tests/fixtures.ts like the other auth-project
// specs, so apply the same reduced-motion emulation.
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

// ut-docs#2935: a display board is watched, not touched. Past the idle
// window it must keep running (no jump to the keypad, its poll still
// answers), while any other screen asks for the PIN — nobody walking past
// can use the till as whoever opened the board.
test.describe('idle display board goes board-only (ut-docs#2935)', () => {
  test.setTimeout(180_000);

  test.afterEach(async ({ page }) => {
    await ensureOperator(page);
    await page.request.post('/api/settings/idle-lock', { form: { minutes: '10' } });
  });

  test('an untouched order board keeps running; Settings asks for the PIN', async ({ page }) => {
    await ensureOperator(page);
    const set = await page.request.post('/api/settings/idle-lock', { form: { minutes: '1' } });
    expect(set.ok(), `set idle-lock: ${set.status()}`).toBe(true);

    await page.goto('/orders');
    await expect(page.locator('[data-display-board]')).toBeAttached();
    await expect(page.locator('body')).toHaveAttribute('data-idle-lock', '60');

    // No input for well past the window (timer 60 s, checked every 5 s).
    await page.waitForTimeout(80_000);
    expect(new URL(page.url()).pathname).toBe('/orders');
    await expect(page.locator('#orders')).toBeAttached();

    // The board's own poll, sent from the page (cookie + Referer), still answers.
    const poll = await page.evaluate(async () => {
      const r = await fetch('/ui/orders', { headers: { 'HX-Request': 'true' } });
      return r.status;
    });
    expect(poll).toBe(200);

    // A full reload (F5, a WebView reload) fires the shell's load-time
    // requests too; the idle board must come back as the board.
    await page.reload();
    await expect(page.locator('#orders .card, #orders table, #orders')).toBeAttached();
    await page.waitForTimeout(3_000);
    expect(new URL(page.url()).pathname).toBe('/orders');

    // Anything else from the board goes to the keypad...
    await page.goto('/settings');
    await expect(page).toHaveURL(/\/login/);
    await expect(page.locator('form[action="/api/auth/login"]')).toBeVisible();
    // ...and the session is gone, board included.
    await page.goto('/orders');
    await expect(page).toHaveURL(/\/login/);
  });
});
