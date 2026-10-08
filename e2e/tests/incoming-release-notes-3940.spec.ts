import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3940: before installing an update, Settings → Software update shows
// the INCOMING version's release notes — every version the update includes,
// newest first — downloaded from the release's release-notes.json. Driven on
// the `incoming-notes` project's till (run-till-incoming-notes.sh): built as
// the newest version with notes and pointed at a fake releases API
// (fake-release-server.mjs) that offers two synthetic newer versions. When
// the notes can't be loaded the card says so and installing still works.
//
// Serial: both tests switch the one fake server's mode. The cashier gate
// (template + endpoint) is covered by the Go tests
// (TestUpdateNotes_CashierRefusedBeforeAnyNetwork,
// TestSettingsUpdateCard_IncomingNotesNeedPluginManagement): this till runs
// with auth off, so there is no cashier session to drive here.
test.describe.configure({ mode: 'serial' });

const FAKE = 'http://127.0.0.1:8100';
const SHOTS = process.env.UT_E2E_SHOTS_DIR;

// Settings' own "Check for updates" makes the till poll the (fake) releases
// API now instead of 30 s after boot; the reload then renders the card with
// the update on offer.
async function checkForUpdates(page: Page) {
  await page.goto('/settings#settings-update');
  const check = page.locator('#settings-update button[hx-post="/api/update/check"]');
  await check.click();
  await expect(page.locator('#update-check-msg')).not.toBeEmpty();
  await page.reload();
}

async function installControlsFollow(page: Page, beforeSelector: string) {
  return page.evaluate((sel) => {
    const notes = document.querySelector(sel);
    const check = document.querySelector('#settings-update button[hx-post="/api/update/check"]');
    return !!notes && !!check && !!(notes.compareDocumentPosition(check) & Node.DOCUMENT_POSITION_FOLLOWING);
  }, beforeSelector);
}

async function shoot(page: Page, name: string) {
  if (!SHOTS) return;
  const card = page.locator('#settings-update');
  const viewport = page.viewportSize()!;
  for (const [w, h, tag] of [[1024, 600, '1024x600'], [360, 740, '360']] as const) {
    await page.setViewportSize({ width: w, height: h });
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${SHOTS}/${name}-${tag}.png`, fullPage: false });
  }
  await page.setViewportSize(viewport);
}

test('a manager sees the incoming versions\' notes, newest first, above the install controls', async ({ page, request }) => {
  const done = watchConsole(page);
  expect((await request.post(`${FAKE}/_mode?m=ok`)).ok()).toBeTruthy();
  const info = await (await request.get(`${FAKE}/_info`)).json();
  const [offered, skipped] = info.versions as string[];

  await checkForUpdates(page);
  const card = page.locator('#settings-update');
  await expect(card.getByRole('heading', { level: 3 })).toContainText(offered);

  const notes = card.locator('#incoming-release-notes').getByTestId('incoming-release-note');
  await expect(notes).toHaveCount(2);
  await expect(notes.nth(0)).toHaveAttribute('data-version', offered);
  await expect(notes.nth(1)).toHaveAttribute('data-version', skipped);
  await expect(notes.nth(0)).toContainText(`Synthetic e2e note for ${offered}`);
  await expect(notes.nth(0).locator('h5').first()).toBeVisible();
  // Never the running version's own note: that one is already installed.
  await expect(card.locator(`[data-testid="incoming-release-note"][data-version="${info.running}"]`)).toHaveCount(0);
  await expect(card.getByTestId('incoming-notes-unavailable')).toHaveCount(0);

  // Read before installing: the notes sit above the install controls.
  expect(await installControlsFollow(page, '#incoming-release-notes')).toBe(true);
  // Inline, not a modal: nothing traps focus over the page.
  await expect(page.locator('dialog[open]:modal')).toHaveCount(0);

  await shoot(page, 'incoming-notes');
  done();
});

test('notes that cannot be loaded say so, and installing still works', async ({ page, request }) => {
  expect((await request.post(`${FAKE}/_mode?m=broken`)).ok()).toBeTruthy();

  await checkForUpdates(page);
  const card = page.locator('#settings-update');
  const unavailable = card.getByTestId('incoming-notes-unavailable');
  await expect(unavailable).toBeVisible();
  await expect(unavailable).toContainText('Release notes unavailable');
  await expect(card.getByTestId('incoming-release-note')).toHaveCount(0);

  // The install controls are untouched: Check now still offers the update.
  const check = card.locator('button[hx-post="/api/update/check"]');
  await expect(check).toBeVisible();
  expect(await installControlsFollow(page, '#incoming-release-notes')).toBe(true);
  await check.click();
  await expect(page.locator('#update-check-msg')).toContainText(/v\d+\.\d+\.\d+/);

  await shoot(page, 'incoming-notes-unavailable');
});
