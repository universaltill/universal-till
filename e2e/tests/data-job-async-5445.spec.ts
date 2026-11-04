import { test, expect } from './fixtures';

// ut-docs#5445: the Settings -> Data actions run as a background job. The
// start answers 202 {job_id}; the card polls GET /api/data/jobs/{id}. This
// holds the poll at "running" for longer than the 30 s prompt timeout and
// asserts the operator sees no "outcome unknown", that the persistent line
// and the locked controls stay up, and that the result appears on release.
// Start and poll are stubbed in the browser (no PIN or real reset needed).

const RUNNING = 'Working on it. This can take a few minutes. You can leave this page and come back to Data management to see the result.';
const JOB = '0123456789abcdef0123456789abcdef';
const json = (status: number, body: unknown) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

test('a job running past the prompt timeout shows progress, then the result (no outcome_unknown)', async ({ page }) => {
  let release!: () => void;
  const released = new Promise<void>((r) => (release = r));
  await page.route('**/api/data/reset-transactions', (route) =>
    route.fulfill(json(202, { data: { job_id: JOB, action: 'reset_transactions', state: 'running' }, error: null })));
  await page.route('**/api/data/jobs/*', async (route) => {
    let done = false;
    await Promise.race([released.then(() => { done = true; }), new Promise((r) => setTimeout(r, 1500))]);
    return route.fulfill(done
      ? json(200, { data: { job_id: JOB, action: 'reset_transactions', state: 'done', http_status: 200, result: { data: { archived: 3, batch_id: 'b1', message: 'x' }, error: null } }, error: null })
      : json(200, { data: { job_id: JOB, action: 'reset_transactions', state: 'running' }, error: null }));
  });

  await page.goto('/settings#settings-data');
  const status = page.locator('#data-job-msg');
  await expect(status).toHaveAttribute('role', 'status');
  await expect(status).toHaveText('');

  await page.locator('[data-testid=data-reset]').click();
  await expect(status).toHaveText(RUNNING);
  await expect(status).toBeFocused();
  for (const el of await page.locator('#settings-data [data-job-lock]').all()) await expect(el).toBeDisabled();
  await expect(page.locator('#elevation-modal')).not.toHaveJSProperty('open', true);

  // Longer than the 30 s prompt timeout: still running, never outcome_unknown.
  await page.waitForTimeout(35000);
  await expect(status).toHaveText(RUNNING);
  await expect(page.locator('#data-reset-msg .pos-notice.error')).toHaveCount(0);

  release();
  await expect(page.locator('#data-reset-msg .pos-notice.success')).toBeVisible();
  await expect(status).toHaveText('');
  await expect(page.locator('#data-reset-btn')).toBeEnabled();
});

test('a second start while busy shows the busy text and leaves the controls usable', async ({ page }) => {
  await page.route('**/api/data/reset-transactions', (route) =>
    route.fulfill(json(409, { data: { code: 'job_busy' }, error: 'Another data action is still running. Wait for it to finish, then try again.' })));
  await page.goto('/settings#settings-data');
  await page.locator('[data-testid=data-reset]').click();
  await expect(page.locator('#data-reset-msg .pos-notice.error .notice-text')).toContainText('Another data action is still running');
  await expect(page.locator('#data-reset-btn')).toBeEnabled();
});
