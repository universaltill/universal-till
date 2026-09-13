import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';
import * as http from 'http';
import * as crypto from 'crypto';

// ut-docs#2169 / ADR-0092 §7: while a diagnostic session is locally active
// the till shows a persistent indicator in the nav rail on EVERY screen —
// a pulse icon with a dot badge (#diagnostics-chip) — and the Settings card
// reads "Diagnostic mode: ON since <date>" with the local stop control next
// to it. This spec drives the REAL activation path end to end against an
// in-process fake ut-cloud (the documented /v1/stores/* contract: register,
// signing key, diagnostics/activate, diagnostics/batch) on the dedicated
// `diagnostics` project's till (run-till-diagnostics.sh points
// UT_MARKETPLACE_ENDPOINT_URL at it), then checks the indicator:
//   - across navigation (/settings → / → /menu),
//   - at 1024×600 (the kiosk floor) and 360×740 (phone width, where the
//     rail becomes a top bar),
//   - in an RTL locale (ar) and an LTR one (en),
//   - as a REAL hit-test target, not just isVisible (same reasoning as
//     nav-rail-lock-reachable-1346.spec.ts),
// and finally that the two-step local stop removes it again.
//
// What this spec does NOT prove, stated plainly: the ADR's "verify on a
// release-mode Android build" item. The indicator is shared server-rendered
// HTML the Android WebView shell shows unchanged, but nothing here runs on a
// device or emulator — that remains a human/local check.

const CLOUD_PORT = 8096;
const CODE = 'e2ecode0000000000000000000000001';
const SESSION_ID = '4f0c9d2e-6a1b-4c3d-8e5f-0a1b2c3d4e5f';

// A real ed25519 public key, hex — enroll.fetchSigningKey validates the
// algorithm and the 32-byte length, so a placeholder string would fail
// registration.
function ed25519PublicKeyHex(): string {
  const { publicKey } = crypto.generateKeyPairSync('ed25519');
  const der = publicKey.export({ type: 'spki', format: 'der' }) as Buffer;
  return der.subarray(der.length - 32).toString('hex');
}

type Cloud = { server: http.Server; batches: number; activations: number };

function startFakeCloud(): Promise<Cloud> {
  const cloud: Cloud = { server: undefined as unknown as http.Server, batches: 0, activations: 0 };
  const pubHex = ed25519PublicKeyHex();
  const json = (res: http.ServerResponse, status: number, body: unknown) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  cloud.server = http.createServer((req, res) => {
    let raw = '';
    req.on('data', (c) => (raw += c));
    req.on('end', () => {
      const url = req.url ?? '';
      const auth = req.headers['authorization'] ?? '';
      let body: Record<string, unknown> = {};
      try {
        body = raw ? JSON.parse(raw) : {};
      } catch {
        body = {};
      }
      if (url === '/v1/stores/register' && req.method === 'POST') {
        return json(res, 201, { data: { store_id: 'e2e-store', merchant_id: 'e2e-merchant', token: 'e2e-token' } });
      }
      if (url === '/ui/api/signing-key') {
        return json(res, 200, { data: { algorithm: 'ed25519', public_key_hex: pubHex } });
      }
      if (url === '/v1/stores/devices/register') {
        return json(res, 200, { data: { ok: true } });
      }
      if (url === '/v1/stores/sync') {
        return json(res, 200, { data: { directives: [] } });
      }
      if (url === '/v1/stores/diagnostics/activate') {
        cloud.activations++;
        if (auth !== 'Bearer e2e-token') return json(res, 401, { error: { code: 'unauthorized', message: 'invalid store token' } });
        if (body['code'] !== CODE) return json(res, 403, { error: { code: 'invalid_code', message: 'no such activation code for this store' } });
        return json(res, 200, { data: { session_id: SESSION_ID } });
      }
      if (url === '/v1/stores/diagnostics/batch') {
        cloud.batches++;
        return json(res, 200, { data: { batch_id: 'b-' + cloud.batches, stored: true } });
      }
      // Every other till→cloud call this tick makes (snapshots, directive
      // results, issue-report status pulls) is accepted as a no-op.
      return json(res, 200, { data: {} });
    });
  });
  return new Promise((resolve) => cloud.server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(cloud)));
}

const chip = (page: Page) => page.locator('#diagnostics-chip [data-testid="diagnostics-chip-on"]');

async function expectChipHitTestable(page: Page, where: string) {
  const c = chip(page);
  await expect(c, `${where}: indicator must be visible`).toBeVisible();
  const a = c.locator('a.nav-toggle');
  await a.scrollIntoViewIfNeeded();
  const hit = await a.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const inView = r.left >= 0 && r.top >= 0 && r.right <= window.innerWidth && r.bottom <= window.innerHeight;
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return inView && !!at && (at === el || el.contains(at));
  });
  expect(hit, `${where}: indicator must be inside the viewport and the real hit-test target`).toBe(true);
}

// /settings is a two-pane page (ut-docs#1960): only the selected section's
// card is shown, so every visit here arrives through the section's own
// inbound hash link, the same way settings-two-pane-1960.spec.ts does.
const DIAG_SECTION = '/settings#settings-diagnostics';

async function ensureRegistered(page: Page) {
  await page.goto('/settings#registration');
  const registerBtn = page.locator('#registration button[hx-post="/api/enrol/now"]');
  if (await registerBtn.count()) {
    await registerBtn.click();
    await expect(page.locator('#enrol-msg')).toContainText('✅');
    await page.goto('/settings#registration');
  }
  await expect(page.locator('#registration')).toContainText('e2e-store');
}

test.describe.serial('diagnostic mode indicator (ut-docs#2169, ADR-0092 §7)', () => {
  let cloud: Cloud;
  test.beforeAll(async () => {
    cloud = await startFakeCloud();
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => cloud.server.close(() => r()));
  });

  test('a redeemed code turns it on and the indicator persists across screens, sizes and locales', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await ensureRegistered(page);
    await page.goto(DIAG_SECTION);

    // Off: no indicator anywhere, the card offers the code entry.
    await expect(page.getByTestId('diagnostics-off')).toBeVisible();
    await expect(chip(page)).toHaveCount(0);

    // A wrong code is refused with a message and nothing turns on.
    await page.getByTestId('diagnostics-code').fill('not-the-code');
    await page.getByTestId('diagnostics-activate').click();
    await expect(page.getByTestId('diagnostics-error')).toBeVisible();
    await expect(page.getByTestId('diagnostics-off')).toBeVisible();
    await expect(chip(page)).toHaveCount(0);

    // The real code: the card flips to ON with the since-date and the stop
    // control, and the rail chip appears in the SAME response (OOB swap),
    // before any 30s poll.
    await page.getByTestId('diagnostics-code').fill(CODE);
    await page.getByTestId('diagnostics-activate').click();
    await expect(page.getByTestId('diagnostics-on')).toContainText('ON since');
    await expect(page.getByTestId('diagnostics-stop')).toBeVisible();
    await expectChipHitTestable(page, '/settings 1024x600 en');
    expect(cloud.activations).toBeGreaterThanOrEqual(2);

    // Persists across navigation — it is the shared nav partial.
    await page.goto('/');
    await expectChipHitTestable(page, '/ 1024x600 en');
    await page.goto('/menu');
    await expectChipHitTestable(page, '/menu 1024x600 en');

    // Phone width: the rail becomes the top bar (<=480px) — still there.
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/settings');
    await expectChipHitTestable(page, '/settings 360x740 en');
    await page.goto('/');
    await expectChipHitTestable(page, '/ 360x740 en');

    // Survives a full reload (server-side state, not a client flag).
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.reload();
    await expectChipHitTestable(page, '/ after reload');
    assertClean();
  });

  test('the indicator lays out in an RTL locale too', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings?lang=ar#settings-diagnostics');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await expectChipHitTestable(page, '/settings 1024x600 ar');
    await expect(page.getByTestId('diagnostics-on')).toBeVisible();
    await page.goto('/?lang=ar');
    await expectChipHitTestable(page, '/ 1024x600 ar');
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/?lang=ar');
    await expectChipHitTestable(page, '/ 360x740 ar');
    // Hand the shared till back to English for the stop test below.
    await page.goto('/?lang=en');
    assertClean();
  });

  test('the local stop shows the disposition, then removes the indicator', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings?lang=en#settings-diagnostics');
    await expect(page.getByTestId('diagnostics-on')).toBeVisible();

    // Step 1: the confirmation names what will be discarded and changes
    // nothing yet; Cancel backs out.
    await page.getByTestId('diagnostics-stop').click();
    await expect(page.getByTestId('diagnostics-stop-confirm')).toBeVisible();
    await expectChipHitTestable(page, 'confirm step still on');
    await page.getByTestId('diagnostics-stop-cancel').click();
    await expect(page.getByTestId('diagnostics-stop-confirm')).toHaveCount(0);
    await expect(page.getByTestId('diagnostics-on')).toBeVisible();

    // Step 2: confirm → OFF immediately, indicator gone in the same
    // response, and gone on every other screen too.
    await page.getByTestId('diagnostics-stop').click();
    await page.getByTestId('diagnostics-stop-confirm-btn').click();
    await expect(page.getByTestId('diagnostics-off')).toBeVisible();
    await expect(page.getByTestId('diagnostics-stopped-notice')).toBeVisible();
    await expect(chip(page)).toHaveCount(0);
    await page.goto('/');
    await expect(page.locator('#diagnostics-chip')).toBeAttached();
    await expect(chip(page)).toHaveCount(0);
    assertClean();
  });
});
