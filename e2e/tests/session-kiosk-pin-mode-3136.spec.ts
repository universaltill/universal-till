import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ADMIN_PIN, ensureOperator } from './helpers';

// ut-docs#3136: the device mode follows who signs in. The seeded
// Self-order kiosk user's PIN puts the till into self-order kiosk mode and
// lands on /self-order; any other user's PIN — here the admin, through the
// kiosk's own lock link — puts it back into normal till mode on the sale
// screen. Drives real PIN logins on the auth till (UT_AUTH on).
//
// Needs the `auth` project. Named to sort AFTER login.spec.ts: the auth
// till is shared and serial, and login.spec.ts needs it still first-boot.

const KIOSK_PIN = '135792';

async function typePIN(page: Page, pin: string): Promise<void> {
  for (const d of pin.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
}

test.describe('Kiosk PIN switches the device mode (ut-docs#3136)', () => {
  test('kiosk PIN enters self-order mode; another PIN restores the till', async ({ page }) => {
    await ensureOperator(page); // admin — first-boot wizard or PIN re-login
    const pinResp = await page.request.post('/api/users/kiosk/pin', { form: { pin: KIOSK_PIN } });
    expect(pinResp.ok(), 'set the kiosk user PIN').toBe(true);

    try {
      await page.request.post('/api/auth/logout');
      await page.goto('/login');
      await typePIN(page, KIOSK_PIN);
      await expect(page).toHaveURL(/\/self-order$/);

      // Persisted, not just a redirect: a fresh "/" (what every kiosk
      // launcher opens) lands on the kiosk too, and no session was kept.
      await page.goto('/');
      await expect(page).toHaveURL(/\/self-order/);
      const settings = await page.request.get('/settings', { maxRedirects: 0 });
      expect(settings.status(), 'the kiosk user holds no session').toBe(303);

      // The kiosk's own lock link, then the admin's PIN.
      await page.locator('a[href="/login?next=kiosk"]').first().click();
      await expect(page).toHaveURL(/\/login\?next=kiosk/);
      await typePIN(page, ADMIN_PIN);
      await expect(page).toHaveURL(/\/$/);
      await expect(page.locator('#basket')).toBeVisible();
    } finally {
      // Leave the shared auth till in register mode with an admin session,
      // whatever happened above: an admin login through the kiosk's lock
      // link (next=kiosk) restores till mode. If an earlier run died before
      // reaching this, ensureOperator above lands on /self-order and the
      // PIN call fails — this finally then repairs the till for the next run.
      await page.request.post('/api/auth/login', { form: { pin: ADMIN_PIN, next: 'kiosk' } });
    }
  });
});
