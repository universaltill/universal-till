import { test, expect } from '@playwright/test';
import { ensureOperator } from './helpers';

// ut-docs#2223: exempt from tests/fixtures.ts like the other auth-project
// specs, so apply the same reduced-motion emulation.
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

// ut-docs#3005: the idle auto-lock looped on the tablet -- a whole-screen
// reload every 10 minutes, never locking. app.js's timer counts from the
// last input OR page load, but the server's idle clock is moved by any
// request that touches the session. After a request made without any input
// (a page load's own follow-ups, here one explicit GET), the server trails
// the client: GET /login found the session "fresh", bounced to "/", and the
// page reloaded. The timer now POSTs /api/auth/idle-lock first, so the till
// lands on the keypad and stays there.
test.describe('idle auto-lock locks instead of looping (ut-docs#3005)', () => {
  test.setTimeout(180_000);

  test.afterEach(async ({ page }) => {
    // Back to the default 10-minute lock for the rest of the auth project.
    await ensureOperator(page);
    await page.request.post('/api/settings/idle-lock', { form: { minutes: '10' } });
  });

  test('an untouched sale screen lands on the PIN keypad and stays there', async ({ page }) => {
    await ensureOperator(page);
    const set = await page.request.post('/api/settings/idle-lock', { form: { minutes: '1' } });
    expect(set.ok(), `set idle-lock: ${set.status()}`).toBe(true);

    await page.goto('/');
    await expect(page.locator('body')).toHaveAttribute('data-idle-lock', '60');
    // No input from here on. 15 s later a request touches the session, so
    // the server's idle clock now trails the page's timer by 15 s.
    await page.waitForTimeout(15_000);
    expect((await page.request.get('/ui/basket')).ok()).toBe(true);

    // The timer fires ~60-65 s after the load.
    await page.waitForURL((u) => u.pathname === '/login', { timeout: 90_000 });
    await expect(page.locator('form[action="/api/auth/login"]')).toBeVisible();
    // Before the fix the server said "fresh" and bounced straight back to
    // "/"; the keypad must still be there a while later.
    await page.waitForTimeout(8_000);
    expect(new URL(page.url()).pathname).toBe('/login');
    await expect(page.locator('form[action="/api/auth/login"]')).toBeVisible();
    // And the session really is gone: the sale screen asks for the PIN.
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
  });
});
