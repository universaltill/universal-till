import { test, expect } from './fixtures';
import { execFileSync, spawn, type ChildProcess } from 'child_process';
import fs from 'fs';
import http from 'http';
import os from 'os';
import path from 'path';
import { PREBUILT_BIN, REPO_ROOT, isHealthy } from './worker-till';

// ut-docs#7171: Settings > Backups > "Keep a protected copy of your backup key
// in the cloud" > manager PIN > Confirm did NOTHING on a real till: the dialog
// stayed. The first request's hint replaced the whole #backup-cloud-card
// (hx-swap outerHTML), so the dialog's retry had no target left and htmx never
// sent it. Each test boots its own till (paired, entitled, holding a key,
// choice not made yet) against a fake cloud, with a manager PIN user.

const PORT = Number(process.env.UT_E2E_KEYCHOICE_PORT_BASE) || 9431;
const STORE = 'st_e2e7171';
const PIN = '482913';

async function fakeCloud(port: number, mode: 'ok' | 'down') {
  const hits: string[] = [];
  const srv = http.createServer((req, res) => {
    req.resume();
    hits.push(`${req.method} ${req.url}`);
    if (mode === 'down' && req.url!.includes('/backups/key')) { res.writeHead(503); res.end('{}'); return; }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ generation: null, fingerprint: null, key_in_cloud: false, escrow_blocked: false }));
  });
  await new Promise<void>((r) => srv.listen(port, '127.0.0.1', () => r()));
  return { hits, close: () => srv.close() };
}

function bootTill(port: number, cloudPort: number) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ut-keychoice-'));
  const env = { ...process.env, UT_DATA_DIR: dataDir };
  const set = (k: string, v: string) =>
    execFileSync('go', ['run', './e2e/till_setting', 'set', k, v], { cwd: REPO_ROOT, env, stdio: ['ignore', 'ignore', 'inherit'] });
  const now = new Date().toISOString();
  set('entitlement.plan', 'local_plus');
  set('entitlement.subscription_status', 'active');
  set('entitlement.last_confirmed_at', now);
  execFileSync('go', ['run', './e2e/offsite_key', STORE], { cwd: REPO_ROOT, env, stdio: ['ignore', 'ignore', 'inherit'] });
  const bin = fs.existsSync(PREBUILT_BIN) ? PREBUILT_BIN : path.join(dataDir, '.ut-bin');
  if (bin !== PREBUILT_BIN) execFileSync('go', ['build', '-o', bin, REPO_ROOT], { cwd: REPO_ROOT, stdio: 'inherit' });
  const child: ChildProcess = spawn(bin, [], {
    cwd: dataDir,
    stdio: 'ignore',
    env: {
      ...env, UT_LISTEN_ADDR: `127.0.0.1:${port}`, UT_OPEN_BROWSER: '0', UT_LAN_DISCOVERY: '0',
      UT_UPDATE_CHECK: '0', UT_MARKETPLACE_ENDPOINT_URL: `http://127.0.0.1:${cloudPort}`,
      UT_MARKETPLACE_STORE_ID: STORE, UT_MARKETPLACE_MERCHANT_TOKEN: 'tok',
    },
  });
  const url = `http://127.0.0.1:${port}`;
  return {
    url,
    async ready() {
      for (let i = 0; i < 120; i++) {
        if (await isHealthy(url)) return;
        await new Promise((r) => setTimeout(r, 500));
      }
      throw new Error(`till ${url} did not come up`);
    },
    async stop() {
      const exited = new Promise<void>((r) => child.once('exit', () => r()));
      child.kill('SIGTERM'); // our own child, by handle
      await Promise.race([exited, new Promise((r) => setTimeout(r, 5000))]);
      fs.rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

// First boot through the real /api/auth/setup (admin PIN), then the session
// cookie goes into the browser context. The admin PIN is the approver PIN.
async function signIn(context: import('@playwright/test').BrowserContext, base: string) {
  const res = await fetch(`${base}/api/auth/setup`, {
    method: 'POST', redirect: 'manual', headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ pin: PIN, pin_confirm: PIN, store_name: 'E2E Shop', till_name: 'E2E Till' }),
  });
  const c = res.headers.getSetCookie().find((h) => h.startsWith('ut_session='));
  expect(c, `first boot ${res.status}`).toBeTruthy();
  await context.addCookies([{ name: 'ut_session', value: c!.split(';')[0].split('=')[1], url: base }]);
}

for (const [i, mode] of (['ok', 'down'] as const).entries()) {
  test(`key choice "on" with a manager PIN closes the dialog and updates the card (cloud ${mode})`, async ({ page }) => {
    test.setTimeout(120_000);
    expect(await isHealthy(`http://127.0.0.1:${PORT + i}`), 'port already serving a till').toBe(false);
    let cloud: Awaited<ReturnType<typeof fakeCloud>> | undefined;
    let till: ReturnType<typeof bootTill> | undefined;
    try {
      cloud = await fakeCloud(PORT + 50 + i, mode);
      till = bootTill(PORT + i, PORT + 50 + i);
      await till.ready();
      await signIn(page.context(), till!.url);
      await page.goto(`${till!.url}/settings#settings-backup`);
      const card = page.locator('[data-testid="backup-cloud-card"]');
      await expect(page.locator('[data-testid="cloud-key-ask"]')).toBeVisible();
      await page.locator('[data-testid="cloud-key-choose-on"]').click();

      const dlg = page.locator('#elevation-modal');
      await expect(dlg).toBeVisible();
      // The card must stay on the page while the dialog is up.
      await expect(card).toHaveCount(1);
      await expect(page.locator('[data-testid="cloud-key-ask"]')).toBeVisible(); // card intact under the dialog
      await expect(page.locator('#backup-cloud-msg .elevation-hint')).toHaveCount(1); // hint in the span
      await dlg.locator('input[name="override_pin"]').fill(PIN);
      await dlg.locator('button[type="submit"]').click();

      await expect(dlg).toBeHidden({ timeout: 20_000 });
      await expect(card).toHaveCount(1);
      await expect(page.locator('[data-testid="cloud-key-ask"]')).toHaveCount(0);
      await expect(page.locator('[data-testid="cloud-key-status"]')).toBeVisible();
      if (mode === 'down') expect(cloud!.hits.some((h) => h.startsWith('GET ') && h.includes('/backups/key')), 'till asked the cloud').toBe(true);
      if (mode === 'down') await expect(page.locator('[data-testid="backup-cloud-notice"]')).toHaveText(/./);
    } finally {
      await till?.stop();
      cloud?.close();
    }
  });
}
