import { test, expect } from './fixtures';
import { watchConsole, nameShop } from './helpers';
import * as http from 'http';
import * as crypto from 'crypto';

// ut-docs#4494 (ADR-0162 §4): a fresh till runs the setup wizard before it
// can be paired, so when a cloud-master store's catalogue pull first runs
// the till already holds migration 001's seed tax codes ("Standard VAT",
// "Reduced VAT", "Zero-rated"; tax_codes.name is UNIQUE). A cloud tax code
// named "Standard VAT" must adopt the seed row rather than be refused (and
// take every item naming it down with it).
//
// Drives the real path: the till registers against an in-process fake
// ut-cloud whose check-in answers an ACTIVE paid entitlement with
// catalog_master "cloud"; the same check-in tick then pulls the catalogue
// feed (GET /v1/stores/catalog, contract cloud-catalog-feed.md) from the
// fake: a "Standard VAT" tax code at 19 %, a category, and an item in it
// with that tax code. The item must show on /catalog, and /catalog/tax-codes
// must list exactly one "Standard VAT", at 19 %.
//
// Runs in the `diagnostics` project (run-till-diagnostics.sh points the till
// at 127.0.0.1:8096, auth off), the one project whose till may register with
// a fake cloud; workers: 1, so the port is never shared with 2169/4285/4286.
// The other specs there never look at the catalogue.

const CLOUD_PORT = 8096;

const TAX_ID = '4494a000-0000-4000-8000-000000000001';
const CAT_ID = '4494a000-0000-4000-8000-000000000002';
const ITEM_ID = '4494a000-0000-4000-8000-000000000003';
const ITEM_NAME = 'Cloud Espresso 4494';

const FEED_ROWS = [
  {
    kind: 'tax_code', cloud_id: TAX_ID, version: 1, deleted: false,
    data: { name: 'Standard VAT', rate_basis_points: 1900, takeaway_rate_basis_points: null, active: true },
  },
  {
    kind: 'category', cloud_id: CAT_ID, version: 2, deleted: false,
    data: {
      name: 'Cloud Drinks 4494', parent_cloud_id: null, sort_order: 0, color: '', icon: '',
      sell_screen_hidden: false, active: true, modifier_group_cloud_ids: [],
    },
  },
  {
    kind: 'item', cloud_id: ITEM_ID, version: 3, deleted: false,
    data: {
      name: ITEM_NAME, sku: '', description: '', unit: 'each', price_minor: 280,
      category_cloud_id: CAT_ID, tax_code_cloud_id: TAX_ID, active: true, is_weighed: false,
      stock_untracked: true, age_restricted: false, color: '', icon: '', item_type: 'standard',
      sell_screen_hidden: false, sell_screen_removed: false, net_quantity_value: null, net_quantity_unit: null,
      modifier_group_cloud_ids: [], modifier_opt_out_cloud_ids: [], option_set_cloud_ids: [],
    },
  },
];
const HIGH_WATER = 3;

function ed25519PublicKeyHex(): string {
  const { publicKey } = crypto.generateKeyPairSync('ed25519');
  const der = publicKey.export({ type: 'spki', format: 'der' }) as Buffer;
  return der.subarray(der.length - 32).toString('hex');
}

type Cloud = { server: http.Server; feedReads: number };

function startCloudMasterCloud(): Promise<Cloud> {
  const pubHex = ed25519PublicKeyHex();
  const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z');
  const json = (res: http.ServerResponse, status: number, body: unknown) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  const cloud: Cloud = { server: undefined as unknown as http.Server, feedReads: 0 };
  cloud.server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const url = new URL(req.url ?? '/', `http://127.0.0.1:${CLOUD_PORT}`);
      if (url.pathname === '/v1/stores/register' && req.method === 'POST') {
        return json(res, 201, { data: { store_id: 'e2e-store', merchant_id: 'e2e-merchant', token: 'e2e-token' } });
      }
      if (url.pathname === '/ui/api/signing-key') {
        return json(res, 200, { data: { algorithm: 'ed25519', public_key_hex: pubHex } });
      }
      if (url.pathname === '/v1/stores/devices/register') {
        return json(res, 200, { data: { ok: true } });
      }
      if (url.pathname === '/v1/stores/sync') {
        return json(res, 200, {
          data: {
            directives: [],
            entitlement: {
              plan: 'shop',
              subscription_status: 'active',
              expires_at: iso(Date.now() + 30 * 864e5),
              refreshed_at: iso(Date.now()),
              cloud_link: 'periodic',
              catalog_master: 'cloud',
            },
          },
        });
      }
      if (url.pathname === '/v1/stores/catalog' && req.method === 'GET') {
        // The feed contract: the till's own credential, its own store.
        if (req.headers['authorization'] !== 'Bearer e2e-token') {
          return json(res, 401, { data: null, error: { code: 'unauthorized', message: 'unauthorized' } });
        }
        if (url.searchParams.get('store_id') !== 'e2e-store') {
          return json(res, 400, { data: null, error: { code: 'invalid_request', message: 'store_id' } });
        }
        cloud.feedReads++;
        const since = Number(url.searchParams.get('since') ?? '0');
        if (since > HIGH_WATER) {
          return json(res, 410, { data: null, error: { code: 'resync', message: 'resync' } });
        }
        const rows = FEED_ROWS.filter((r) => r.version > since);
        return json(res, 200, { data: { rows, next: HIGH_WATER, high_water: HIGH_WATER }, error: null });
      }
      if (url.pathname === '/v1/stores/devices') {
        return json(res, 200, { data: { devices: [] } });
      }
      // Snapshots, directive results, issue-report pulls: accepted no-ops.
      return json(res, 200, { data: {} });
    });
  });
  return new Promise((resolve) => cloud.server.listen(CLOUD_PORT, '127.0.0.1', () => resolve(cloud)));
}

test.describe.serial('cloud catalogue bootstrap over the seed rows (ut-docs#4494)', () => {
  let cloud: Cloud;
  test.beforeAll(async () => {
    cloud = await startCloudMasterCloud();
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => cloud.server.close(() => r()));
  });

  test('a cloud "Standard VAT" adopts the seed tax code and its item applies', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });

    // The seed rows are there before the till is paired.
    await page.goto('/catalog/tax-codes');
    await expect(page.locator('tr.tax-code-row[data-name="Standard VAT"]')).toHaveCount(1);

    // Register (a fresh diagnostics till) — registering checks in at once,
    // and that check-in tick caches catalog_master "cloud" and then pulls
    // the feed. An already-registered till asks via "Check for a paid plan".
    await page.goto('/settings#registration');
    const card = page.locator('#registration');
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

    // The pulled item shows on the catalogue once the pull has run.
    await expect(async () => {
      await page.goto('/catalog');
      await expect(page.getByText(ITEM_NAME, { exact: true }).first()).toBeVisible({ timeout: 1000 });
    }).toPass({ timeout: 30_000 });
    expect(cloud.feedReads, 'the till read the catalogue feed').toBeGreaterThan(0);

    // Exactly one "Standard VAT" — the seed row, now at the cloud's 19 %.
    await page.goto('/catalog/tax-codes');
    const std = page.locator('tr.tax-code-row[data-name="Standard VAT"]');
    await expect(std).toHaveCount(1);
    await expect(std).toHaveAttribute('data-id', 'tax_std');
    await expect(std).toHaveAttribute('data-rate', '19');
    await expect(std.locator('td').nth(1)).toHaveText('19%');
    await expect(std).toHaveAttribute('data-active', '1');
    assertClean();
  });
});
