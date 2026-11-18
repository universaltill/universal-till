import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#6550: on the Android app "Save to Downloads" never opened the save
// picker. The bridge script lived inline in #ut-page; the shell's
// dropPageListeners() removes the document click listener a page script adds
// on every boosted navigation, while the script's own run-once guard
// (window.utBackupSaveResult) outlived it. So from the second visit to
// Settings the button fell through to the server and printed "press again"
// forever. Only a real browser with the real shell can show that, so this
// installs a recording window.AndroidKiosk stub (same technique as
// android-kiosk-bridge-1254.spec.ts) and drives real boosted navigations.

const stub = `
  window.__saveCalls = [];
  window.AndroidKiosk = { saveBackupCopy: function (n) { window.__saveCalls.push(n); } };
`;

async function boostedVisit(page: Page, href: string, probe: string) {
  // A link inside #ut-page is a boosted (hx-boost) shell navigation: a swap,
  // not a document load, so window.__sentinel must survive.
  await page.evaluate((h) => {
    const a = document.createElement('a');
    a.href = h; a.id = 'e2e-boost-link'; a.textContent = 'go';
    document.getElementById('ut-page')!.appendChild(a);
    (window as any).htmx.process(a);
  }, href);
  await page.locator('#e2e-boost-link').click();
  await expect(page.locator(probe)).toBeVisible();
  expect(await page.evaluate(() => (window as any).__sentinel)).toBe('alive');
}

async function openBackups(page: Page) {
  await page.goto('/settings#settings-backup');
  const region = page.locator('#settings-backup-region');
  await expect(region).toBeVisible();
  if (await region.locator('button[data-backup-save]').count() === 0) {
    await region.locator('button[hx-post="/api/backup/now"]').click();
    await expect(page.locator('#settings-backup-region button[data-backup-save]').first()).toBeVisible();
  }
  await page.evaluate(() => { (window as any).__sentinel = 'alive'; });
}

const saveBtn = (page: Page) => page.locator('#settings-backup-region button[data-backup-save]').first();
const calls = (page: Page) => page.evaluate(() => (window as any).__saveCalls as string[]);

test.describe('ut-docs#6550 Android save picker survives boosted navigation', () => {
  test('Save calls the bridge once per click on every revisit, never the server', async ({ page }) => {
    await page.route('**/api/backup/save-copy/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: 'stubbed' })); // never write the dev's ~/Downloads
    await page.addInitScript(stub);
    const posts: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/backup/save-copy/')) posts.push(r.url()); });
    await openBackups(page);
    const name = (await saveBtn(page).getAttribute('data-backup-save'))!;

    await saveBtn(page).click();
    expect(await calls(page)).toEqual([name]);

    for (let i = 1; i <= 3; i++) {
      await boostedVisit(page, '/audit', '#ut-page');
      await boostedVisit(page, '/settings#settings-backup', '#settings-backup-region');
      await expect(saveBtn(page)).toBeVisible();
      await saveBtn(page).click();
      // exactly one more call: revisits must neither lose nor duplicate the handler
      expect(await calls(page)).toHaveLength(i + 1);
    }
    expect(posts).toEqual([]);

    // A Backup-now region refresh keeps the delegated handler working.
    await page.locator('#settings-backup-region button[hx-post="/api/backup/now"]').click();
    await expect(page.locator('#backup-msg')).toContainText('✓');
    await saveBtn(page).click();
    expect(await calls(page)).toHaveLength(5);
    expect(posts).toEqual([]);
  });

  test('the bridge result shows the message and records the copy, after a revisit', async ({ page }) => {
    await page.addInitScript(stub);
    await openBackups(page);
    await boostedVisit(page, '/audit', '#ut-page');
    await boostedVisit(page, '/settings#settings-backup', '#settings-backup-region');
    const name = (await saveBtn(page).getAttribute('data-backup-save'))!;

    const copied = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/backup/copied/' + encodeURIComponent(name)));
    await page.evaluate((n) => (window as any).utBackupSaveResult('saved', n), name);
    await copied;
    await expect(page.locator('#backup-dl-msg')).toContainText('✓ Backup saved to the place you chose.');

    for (const [code, text] of [
      ['cancelled', '✗ '], ['no_space', '✗ '], ['failed', '✗ '],
    ] as const) {
      await page.evaluate((c) => (window as any).utBackupSaveResult(c, 'x.db'), code);
      await expect(page.locator('#backup-dl-msg')).toContainText(text);
      await expect(page.locator('#backup-dl-msg')).not.toContainText('✓');
    }
  });

  test('Android page without a working bridge: clear message, no server round trip', async ({ page }) => {
    await page.route('**/api/backup/save-copy/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: 'stubbed' })); // never write the dev's ~/Downloads
    const posts: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/backup/save-copy/')) posts.push(r.url()); });
    await openBackups(page);
    // The test till is not Android; mark the button the way an Android-rendered page does.
    await saveBtn(page).evaluate((b) => b.setAttribute('data-backup-native', '1'));
    await saveBtn(page).click();
    await expect(page.locator('#backup-dl-msg')).toContainText('cannot open the file picker');
    expect(posts).toEqual([]);
  });

  test('without the bridge and not Android, the htmx server copy still runs', async ({ page }) => {
    // Stubbed: the real handler writes into the developer's ~/Downloads.
    await page.route('**/api/backup/save-copy/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: 'ok' }));
    const posts: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/backup/save-copy/')) posts.push(r.url()); });
    await openBackups(page);
    await saveBtn(page).click();
    await expect.poll(() => posts.length).toBe(1);
  });
});
