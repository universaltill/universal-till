import { test, expect } from './fixtures';
import { execFileSync } from 'child_process';
import { REPO_ROOT } from './worker-till';

// ut-docs#5607: on a joined till (sync.primary_url set) the Kitchen stations
// dialog shows Name and Destination read-only, keeps the printer address
// editable (the one till-local field), explains why in visible text and
// disables New. Same worker-till pattern as confirm-dialog-escape-focus-5275.

const DIALOG = '#kitchen-station-dialog';

function setting(op: 'set' | 'delete', key: string, value?: string) {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  execFileSync('go', ['run', './e2e/till_setting', op, key, ...(value === undefined ? [] : [value])], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'ignore', 'inherit'],
  });
}

test.describe('kitchen stations on a joined till (ut-docs#5607)', () => {
  const restore = () => {
    if (!process.env.UT_E2E_WORKER_DATA_DIR) return;
    try { setting('delete', 'sync.primary_url'); } catch { setting('delete', 'sync.primary_url'); }
  };
  test.afterEach(restore);
  test.afterAll(restore);

  test('name/destination read-only, address editable and saved, New disabled', async ({ page, workerServerURL }) => {
    void workerServerURL;
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir)');
    const tag = Date.now().toString(36);
    const a = `Joined A ${tag}`;
    const b = `Joined B ${tag}`;
    // Create while still a main till: a joined till refuses creates.
    for (const [n, dest] of [[a, 'printer'], [b, 'both']]) {
      const r = await page.request.post('/api/kitchen-stations', {
        form: { name: n, destination_type: dest, printer_address: '192.0.2.10:9100' },
        maxRedirects: 0,
      });
      expect(r.status()).toBe(303);
    }
    setting('set', 'sync.primary_url', 'http://127.0.0.1:9');

    await page.goto('/kitchen-stations');
    await expect(page.locator('#kitchen-stations-joined-hint')).toBeVisible();
    await expect(page.locator('#kitchen-stations-new')).toBeDisabled();

    const rowOf = (n: string) => page.locator('#kitchen-stations-table .kitchen-station-row', { hasText: n }).first();
    await rowOf(a).locator('[data-record-edit]').click();
    await expect(page.locator(DIALOG)).toHaveJSProperty('open', true);
    await expect(page.locator('#kitchen-station-joined-note')).toBeVisible();
    await expect(page.locator('#kitchen-station-name')).toHaveValue(a);
    await expect(page.locator('#kitchen-station-name')).toHaveJSProperty('readOnly', true);
    await expect(page.locator('#kitchen-station-destination')).toBeDisabled();
    await expect(page.locator('#kitchen-station-destination')).toHaveValue('printer');
    await expect(page.locator('#kitchen-station-address')).toBeEditable();
    await expect(page.locator(`${DIALOG} [data-record-destructive-form]`)).toHaveCount(0);

    await page.locator('#kitchen-station-address').fill('192.0.2.77:9100');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(rowOf(a)).toContainText('192.0.2.77:9100');
    await expect(rowOf(a)).toContainText(a);
    await expect(rowOf(a)).toHaveAttribute('data-field-destination_type', 'printer');

    // Reopening another station re-applies the state with that station's values.
    await rowOf(b).locator('[data-record-edit]').click();
    await expect(page.locator('#kitchen-station-name')).toHaveValue(b);
    await expect(page.locator('#kitchen-station-name')).toHaveJSProperty('readOnly', true);
    await expect(page.locator('#kitchen-station-destination')).toBeDisabled();
    await expect(page.locator('#kitchen-station-destination')).toHaveValue('both');
    await expect(rowOf(b)).toHaveAttribute('data-field-destination_type', 'both');
  });
});
