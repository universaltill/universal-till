import { test, expect } from './fixtures';
import { watchConsole, nameShop } from './helpers';
import * as http from 'http';
import * as crypto from 'crypto';

// ut-docs#5275, pair caller: "Pair with a shop" on a REGISTERED till asks
// "replace this till's identity?" (confirm_prompt.html, hint swapped into
// #pair-msg). Escape and Cancel must send no request, hand focus back to the
// code input, and remove the prompt's own .elevation-hint from #pair-msg.
// Never confirms (that would wipe the identity). Diagnostics project: the
// only till allowed to register with a fake cloud (127.0.0.1:8096).

const CLOUD_PORT = 8096;

function ed25519PublicKeyHex(): string {
  const { publicKey } = crypto.generateKeyPairSync('ed25519');
  const der = publicKey.export({ type: 'spki', format: 'der' }) as Buffer;
  return der.subarray(der.length - 32).toString('hex');
}

function startPaidCloud(): Promise<http.Server> {
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
              subscription_status: 'active',
              expires_at: iso(Date.now() + 30 * 864e5),
              refreshed_at: iso(Date.now()),
              cloud_link: 'periodic',
            },
          },
        });
      }
      if (url.startsWith('/v1/stores/devices?')) {
        return json(res, 200, { data: { devices: [] } });
      }
      // Snapshots, directive results, issue-report pulls: accepted no-ops.
      return json(res, 200, { data: {} });
    });
  });
  return new Promise((resolve) => server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(server)));
}

test.describe.serial('pair confirm Escape / Cancel (ut-docs#5275)', () => {
  let cloud: http.Server;
  test.beforeAll(async () => { cloud = await startPaidCloud(); });
  test.afterAll(async () => { await new Promise<void>((r) => cloud.close(() => r())); });

  test('registers this till against the fake cloud', async ({ page }) => {
    await page.goto('/settings#registration');
    const card = page.locator('#registration');
    const registerBtn = card.locator('button[hx-post="/api/enrol/now"]');
    if (await registerBtn.count()) {
      await nameShop(page);
      await page.locator('#enrol-accept-terms').check();
      await registerBtn.click();
      await expect(page.locator('#enrol-msg')).toContainText('✅');
    }
    await expect(async () => {
      await page.reload();
      await expect(card).toContainText('This till is registered', { timeout: 1000 });
    }).toPass({ timeout: 20_000 });
  });

  for (const how of ['Escape', 'Cancel'] as const) {
    test(`${how}: no request, focus on the code input, hint removed`, async ({ page }) => {
      const assertClean = watchConsole(page);
      let posts = 0;
      await page.route('**/api/enrol/pair', (route) => {
        if (route.request().method() === 'POST') posts++;
        return route.continue();
      });
      await page.goto('/settings#registration');
      const code = page.locator('#pair-code');
      await code.fill('K7QX-M2RP');
      await code.press('Enter');
      const dlg = page.locator('#confirm-modal');
      await expect(dlg).toHaveJSProperty('open', true);
      expect(posts).toBe(1);
      // The prompt's own hint sits in the hx-target while the dialog is open.
      await expect(page.locator('#pair-msg .elevation-hint')).toHaveCount(1);
      // Not inert: the status bar stays reachable (no showModal).
      await expect(page.locator('#ut-scrim')).toBeVisible();
      if (how === 'Escape') await page.keyboard.press('Escape');
      else await dlg.locator('[data-action="close-closest"]').click();
      await expect(dlg).toHaveJSProperty('open', false);
      await expect(page.locator('#ut-scrim')).toBeHidden();
      expect(posts).toBe(1);
      await expect(page.locator('#pair-msg .elevation-hint')).toHaveCount(0);
      await expect(code).toBeFocused();
      await expect(code).toHaveValue('K7QX-M2RP');
      assertClean();
    });
  }

  // ut-docs#5288: the confirmed pair POST is not idempotent (it clears the
  // till's identity), so while it is in flight Cancel/Escape are inert and the
  // answer still shows. The held request is fulfilled by the test, never sent
  // to the till: the identity is not wiped.
  test('Escape and Cancel while the confirmed pair request is in flight do nothing (ut-docs#5288)', async ({ page }) => {
    const assertClean = watchConsole(page);
    let posts = 0;
    let release!: () => void;
    const gate = new Promise<void>((r) => { release = r; });
    let held!: () => void;
    const inFlight = new Promise<void>((r) => { held = r; });
    await page.route('**/api/enrol/pair', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      posts++;
      if (posts === 1) return route.continue();
      held();
      await gate;
      return route.fulfill({ status: 200, contentType: 'text/html', body: '<span id="pair-answer-5288">Answer 5288</span>' });
    });
    await page.goto('/settings#registration');
    const code = page.locator('#pair-code');
    await code.fill('K7QX-M2RP');
    await code.press('Enter');
    const dlg = page.locator('#confirm-modal');
    await expect(dlg).toHaveJSProperty('open', true);
    const cancel = dlg.locator('[data-action="close-closest"]');
    await dlg.locator('button[type=submit]').click();
    await inFlight;
    await expect(cancel).toBeDisabled();
    await page.keyboard.press('Escape');
    await cancel.click({ force: true });
    await expect(dlg).toHaveJSProperty('open', true);
    await expect(page.locator('#pair-msg .elevation-hint')).toHaveCount(1);
    expect(posts).toBe(2);
    release();
    await expect(page.locator('#pair-answer-5288')).toHaveText('Answer 5288');
    await expect(dlg).toHaveJSProperty('open', false);
    assertClean();
  });
});
