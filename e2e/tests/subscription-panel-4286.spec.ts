import { test, expect } from './fixtures';
import { watchConsole, nameShop } from './helpers';
import * as http from 'http';
import * as crypto from 'crypto';

// ut-docs#4286: the Renew / Choose-a-plan panel, driven against an
// in-process fake ut-cloud whose check-in answers with a LAPSED plan.
// Opening the panel refreshes from the cloud first (so a till that was
// active — the 4285 spec leaves it paid — learns it lapsed), then offers
// Renew current plan / Choose a plan; Renew shows a QR of
// <cloud base>/plans?…&intent=renew in the same panel; Close returns to the
// page with focus back on the opener. Non-modal: the status bar stays
// clickable while the panel is open.
//
// Runs in the `diagnostics` project (run-till-diagnostics.sh points the till
// at 127.0.0.1:8096, auth off), the one project whose till may register with
// a fake cloud; workers: 1, so the port is never shared with 2169/4285.

const CLOUD_PORT = 8096;
// The till's language is English; its region follows the host locale
// (en-GB / en-US), both of which the cloud ships.
const PLANS_URL = new RegExp(`^http://127\\.0\\.0\\.1:${CLOUD_PORT}/plans\\?intent=renew&lang=en-(gb|us)&store=e2e-store$`);

function ed25519PublicKeyHex(): string {
  const { publicKey } = crypto.generateKeyPairSync('ed25519');
  const der = publicKey.export({ type: 'spki', format: 'der' }) as Buffer;
  return der.subarray(der.length - 32).toString('hex');
}

function startLapsedCloud(): Promise<http.Server> {
  const pubHex = ed25519PublicKeyHex();
  const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z');
  const json = (res: http.ServerResponse, status: number, body: unknown) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const url = req.url ?? '';
      if (url === '/v1/stores/register' && req.method === 'POST') {
        return json(res, 201, { data: { store_id: 'e2e-store', merchant_id: 'e2e-merchant', token: 'e2e-token' } });
      }
      if (url === '/ui/api/signing-key') {
        return json(res, 200, { data: { algorithm: 'ed25519', public_key_hex: pubHex } });
      }
      if (url === '/v1/stores/sync') {
        return json(res, 200, {
          data: {
            directives: [],
            entitlement: {
              plan: 'shop',
              subscription_status: 'lapsed',
              expires_at: iso(Date.now() - 2 * 864e5),
              refreshed_at: iso(Date.now()),
              cloud_link: 'periodic',
            },
          },
        });
      }
      if (url.startsWith('/v1/stores/devices?')) {
        return json(res, 200, { data: { devices: [] } });
      }
      return json(res, 200, { data: {} });
    });
  });
  return new Promise((resolve) => server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(server)));
}

test.describe.serial('Renew / Choose-a-plan panel (ut-docs#4286)', () => {
  let cloud: http.Server;
  test.beforeAll(async () => {
    cloud = await startLapsedCloud();
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => cloud.close(() => r()));
  });

  for (const size of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
    test(`refresh first, choices, QR, close at ${size.width}x${size.height}`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(size);
      // Registered against the fake cloud (an earlier spec may already have).
      await page.goto('/settings#registration');
      const registerBtn = page.locator('#registration button[hx-post="/api/enrol/now"]');
      if (await registerBtn.count()) {
        await nameShop(page); // ut-docs#4272: registration waits for a real shop name
        await registerBtn.click();
        await expect(page.locator('#enrol-msg')).toContainText('✅');
      }
      // A hash-only goto is a same-document navigation: reload so the
      // card renders for the now-registered till (the opener is only for a
      // registered one).
      await page.goto('/settings#subscription');
      await page.reload();
      const opener = page.locator('#subscription').getByTestId('subscription-renew-open');
      await expect(opener).toBeVisible();
      await opener.click();

      const panel = page.getByTestId('subscription-panel');
      await expect(panel).toBeVisible();
      // Non-modal: opened with show(), the shared scrim is up, and the
      // status bar is above it.
      expect(await panel.evaluate((d) => (d as HTMLDialogElement).matches(':modal'))).toBe(false);
      await expect(page.locator('#ut-scrim')).toBeVisible();
      const barZ = await page.locator('.statusbar').evaluate((el) => Number(getComputedStyle(el).zIndex));
      expect(barZ).toBeGreaterThan(450);

      // Refresh first: the cloud says lapsed → the two choices.
      const choices = panel.getByTestId('subscription-panel-choices');
      await expect(choices).toBeVisible({ timeout: 15_000 });
      await expect(panel.getByTestId('subscription-panel-choose')).toBeVisible();
      const renew = panel.getByTestId('subscription-panel-renew');
      await expect(renew).toBeVisible();
      await renew.click();

      // Step 3: QR + the exact URL, re-check armed.
      await expect(panel.getByTestId('subscription-panel-qr')).toBeVisible();
      await expect(panel.getByTestId('subscription-panel-url')).toHaveText(PLANS_URL);
      await expect(panel.getByTestId('subscription-panel-poll')).toHaveAttribute('hx-trigger', 'every 20s');

      // The panel fits the viewport (its body scrolls on a small screen).
      const box = await panel.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(size.width);
      expect(box!.y + box!.height).toBeLessThanOrEqual(size.height);

      // Back → choices without another refresh; then Close.
      await panel.getByTestId('subscription-panel-back').click();
      await expect(choices).toBeVisible();
      await panel.getByTestId('subscription-panel-close').click();
      await expect(panel).toBeHidden();
      await expect(page.locator('#ut-scrim')).toBeHidden();
      await expect(page).toHaveURL(/\/settings/);
      // Focus is back on the opener (a lapsed till re-renders the card on
      // ut-subscription-changed only for a renewal, so it is still there).
      await expect(opener).toBeFocused();
      assertClean();
    });
  }

  test('the status-bar chip opens the same panel, Escape closes it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    const chip = page.locator('#sb-subscription-mount').getByTestId('sb-subscription');
    await expect(chip).toBeVisible({ timeout: 15_000 });
    await expect(chip).toHaveAttribute('data-sub-state', 'lapsed');
    await chip.click();
    const panel = page.getByTestId('subscription-panel');
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId('subscription-panel-choices')).toBeVisible({ timeout: 15_000 });
    await expect(page).toHaveURL(/127\.0\.0\.1:8095\/?$/);
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    assertClean();
  });
});
