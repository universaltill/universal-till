import { test, expect } from './fixtures';
import { watchConsole, nameShop } from './helpers';
import * as http from 'http';
import * as crypto from 'crypto';

// ut-docs#4285 (review finding): the renewal re-read once sat on
// <div class="card" id="registration"> as hx-get/hx-select/hx-target. htmx
// INHERITS hx-select and hx-target, so every htmx control inside the card
// filtered its own response down to "#registration" (absent from a partial)
// and swapped in nothing — on a PAID till the fleet list
// (hx-get="/api/enrol/devices" hx-trigger="load") blanked on page load. The
// re-read now lives on a hidden sibling. This spec drives a really paid till:
// registered against an in-process fake ut-cloud whose sync answers 200 with
// an ACTIVE entitlement block, then checks the card renders its registered
// state with the fleet list loaded inside it.
//
// Runs in the `diagnostics` project (run-till-diagnostics.sh points the till
// at 127.0.0.1:8096), the one project whose till is allowed to register with
// a fake cloud; the shared default till must stay unregistered.

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

test.describe.serial('registration card on a paid till (ut-docs#4285)', () => {
  let cloud: http.Server;
  test.beforeAll(async () => {
    cloud = await startPaidCloud();
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => cloud.close(() => r()));
  });

  test('the card is not blanked: registered text and the fleet list load inside it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings#registration');
    const card = page.locator('#registration');

    // Get the till onto a paid plan through the real cloudsync path:
    // registering checks in at once; an already-registered till (the
    // diagnostics spec ran first) asks via "Check for a paid plan".
    const registerBtn = card.locator('button[hx-post="/api/enrol/now"]');
    if (await registerBtn.count()) {
      await nameShop(page); // ut-docs#4272: registration waits for a real shop name
      await page.locator('#enrol-accept-terms').check(); // ut-docs#4720: the cloud terms are accepted first
      await registerBtn.click();
      await expect(page.locator('#enrol-msg')).toContainText('✅');
    } else {
      const checkPlan = card.locator('button[hx-post="/api/enrol/check-plan"]');
      if (await checkPlan.count()) {
        await checkPlan.click();
        await expect(page.locator('#check-plan-msg')).toContainText('✅');
      }
    }

    // The fleet list is rendered only once the cached entitlement allows
    // cloud sync; reload until the check-in has landed.
    const fleet = card.locator('div[hx-get="/api/enrol/devices"]');
    await expect(async () => {
      await page.reload();
      await expect(fleet).toHaveCount(1, { timeout: 1000 });
    }).toPass({ timeout: 20_000 });

    // The point of the spec: the fleet list's own load request filled its
    // div (before the fix it swapped in nothing and the div went empty).
    await expect(fleet).toContainText('No other tills registered yet.');
    await expect(card).toContainText('This till is registered');
    await expect(card).toContainText('e2e-store');
    await expect(card.getByText('Cloud sync is off', { exact: false })).toHaveCount(0);
    await expect(fleet).toBeVisible();

    // The renewal re-read is a sibling, not attributes on the card.
    await expect(card).not.toHaveAttribute('hx-select', /.*/);
    await expect(card).not.toHaveAttribute('hx-target', /.*/);
    const refresh = page.getByTestId('registration-refresh');
    await expect(refresh).toHaveCount(1);
    await expect(refresh).toHaveAttribute('hx-target', '#registration');
    expect(await refresh.evaluate((el) => !!el.closest('#registration'))).toBe(false);
    assertClean();
  });
});
