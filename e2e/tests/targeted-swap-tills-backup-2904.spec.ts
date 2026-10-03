import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2904: pairing approve/deny, till revoke and Backup now used to
// answer with HX-Refresh (a full page reload). They now re-render only
// their own card via UT.refreshRegion. A marker on `window` proves no
// reload happened; a stale attribute on the old card proves the region
// really was replaced from the server.

// Any 64-character commitment passes POST /api/sync/pair-request's
// validation (tills-pairing-layout-1548.spec.ts uses the same seed).
const SEED_COMMITMENT = 'b'.repeat(64);

test('tills: deny and approve refresh only the pairing card, no page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  const api = page.request;
  for (const name of ['test-2904-deny-till', 'test-2904-approve-till']) {
    const seeded = await api.post('/api/sync/pair-request', {
      data: { device_name: name, commitment: SEED_COMMITMENT },
    });
    expect(seeded.status(), `seeding ${name}`).toBe(200);
  }

  await page.goto('/tills');
  const card = page.locator('#tills-pairing-card');
  const denyRow = card.locator('tr', { hasText: 'test-2904-deny-till' });
  const approveRow = card.locator('tr', { hasText: 'test-2904-approve-till' });
  await expect(denyRow).toHaveCount(1);
  await expect(approveRow).toHaveCount(1);
  await page.evaluate(() => { (window as any).__ut2904 = 'still-here'; });

  await card.evaluate((c) => c.setAttribute('data-stale', '1'));
  await denyRow.getByRole('button', { name: /deny/i }).click();
  await expect(page.locator('#tills-pairing-card[data-stale]')).toHaveCount(0); // card was replaced
  await expect(denyRow).toHaveCount(0);
  await expect(approveRow).toHaveCount(1); // the fresh card's load trigger refetched the list
  expect(await page.evaluate(() => (window as any).__ut2904), 'page was reloaded').toBe('still-here');

  await card.evaluate((c) => c.setAttribute('data-stale', '1'));
  await approveRow.getByRole('button', { name: /approve/i }).click();
  await expect(page.locator('#tills-pairing-card[data-stale]')).toHaveCount(0);
  await expect(approveRow).toHaveCount(0);
  await expect(card.locator('.pairing-table')).toHaveCount(0);
  expect(await page.evaluate(() => (window as any).__ut2904), 'page was reloaded').toBe('still-here');
  assertClean();
});

test('tills: revoke refreshes only the enrolled-tills roster, no page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  const api = page.request;
  // Same two real server calls as nav-rail-lock-reachable-1346.spec.ts:
  // mint an enrolment code, then enrol the way a satellite till does.
  const tokenResp = await api.post('/api/sync/enroll-token');
  expect(tokenResp.ok(), `enroll-token: ${tokenResp.status()}`).toBe(true);
  const codeMatch = (await tokenResp.text()).match(/<code[^>]*>([^<]+)<\/code>/);
  expect(codeMatch, 'expected a <code> enrolment string').not.toBeNull();
  const decoded = JSON.parse(Buffer.from(codeMatch![1].trim(), 'base64url').toString('utf8')) as { token: string };
  const enrollResp = await api.post('/api/sync/enroll', {
    data: { token: decoded.token, name: 'test-2904-revoke-till' },
  });
  expect(enrollResp.ok(), `enroll: ${enrollResp.status()} ${await enrollResp.text()}`).toBe(true);

  await page.goto('/tills');
  const roster = page.locator('#tills-roster');
  const row = roster.locator('tr', { hasText: 'test-2904-revoke-till' });
  await expect(row).toHaveCount(1);
  await page.evaluate(() => { (window as any).__ut2904 = 'still-here'; });
  await roster.evaluate((r) => r.setAttribute('data-stale', '1'));

  page.once('dialog', (d) => d.accept()); // hx-confirm
  await row.getByRole('button', { name: /revoke/i }).click();
  await expect(page.locator('#tills-roster[data-stale]')).toHaveCount(0); // roster was replaced
  await expect(row).toHaveCount(0);
  expect(await page.evaluate(() => (window as any).__ut2904), 'page was reloaded').toBe('still-here');
  assertClean();
});

test('settings: Backup now refreshes only the backup list and keeps its ✓, no page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings#settings-backup');
  const card = page.locator('#settings-backup');
  await expect(card).toBeVisible();
  const fileCells = () => page.locator('#settings-backup tbody tr td:first-child').allTextContents();
  const before = new Set(await fileCells());
  await page.evaluate(() => { (window as any).__ut2904 = 'still-here'; });
  const region = page.locator('#settings-backup-region');
  await region.evaluate((r) => r.setAttribute('data-stale', '1'));

  await region.locator('button[hx-post="/api/backup/now"]').click();
  await expect(page.locator('#settings-backup-region[data-stale]')).toHaveCount(0); // region was replaced
  await expect(page.locator('#backup-msg')).toContainText('✓'); // the action's own message survives the swap
  // The new snapshot is listed (by name — pruning may keep the row count flat).
  await expect.poll(async () => (await fileCells()).some((n) => !before.has(n))).toBe(true);
  await expect(page.locator('#settings-backup')).toBeVisible();
  // Review finding: the section switcher holds the card element, so the
  // region must sit inside the card — switching away must still hide it.
  await page.evaluate(() => { location.hash = '#settings-invoice'; });
  await expect(page.locator('#settings-invoice')).toBeVisible();
  await expect(page.locator('#settings-backup')).toBeHidden();
  await page.evaluate(() => { location.hash = '#settings-backup'; });
  await expect(page.locator('#settings-backup')).toBeVisible();
  expect(await page.evaluate(() => (window as any).__ut2904), 'page was reloaded').toBe('still-here');
  assertClean();
});
