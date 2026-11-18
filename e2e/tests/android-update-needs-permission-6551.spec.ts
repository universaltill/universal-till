import { test, expect } from './fixtures';

// ut-docs#6551: the native shell reports `needs_permission` when Android has
// not allowed this app to install apps. web/public/android-update.js must
// (a) remember it for the Settings page the status chip navigates to,
// (b) clear it on any other state, (c) re-enable the shared install button
// without clobbering its label, and (d) only ever ask the shell to open the
// system screen through the argument-less bridge method. The e2e till is not
// an Android till, so the shell is played by window.utAndroidUpdate and a stub
// window.AndroidKiosk; the script under test is the real one the page loads.
const KEY = 'utAndroidNeedsPermission';

function report(page: import('@playwright/test').Page, state: string, error: string | null = null) {
  return page.evaluate(([s, e]) => (window as any).utAndroidUpdate({ state: s, percent: null, error: e }), [state, error]);
}

test.describe('android-update.js: needs_permission (ut-docs#6551)', () => {
  test('is remembered, tracks still_denied, and is cleared by the next state', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await page.evaluate(() => { const f = document.createElement('form'); f.id = 'android-update-form'; f.hidden = true; document.body.appendChild(f); });

    await report(page, 'needs_permission');
    expect(await page.evaluate((k) => sessionStorage.getItem(k), KEY)).toBe('1');
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.pendingPermission())).toBe('1');

    await report(page, 'needs_permission', 'still_denied');
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.pendingPermission())).toBe('denied');

    await report(page, 'permission_granted');
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.pendingPermission())).toBeNull();
  });

  test('the check-result button keeps its label and stays tappable', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await page.evaluate(() => { const f = document.createElement('form'); f.id = 'android-update-form'; f.hidden = true; document.body.appendChild(f); });
    await page.evaluate(() => {
      const b = document.createElement('button');
      b.type = 'button';
      b.setAttribute('data-android-update-install', '');
      b.setAttribute('data-testid', 'android-update-install');
      b.disabled = true;
      b.textContent = 'Download (test)';
      document.body.appendChild(b);
    });
    await report(page, 'needs_permission');
    const btn = page.getByTestId('android-update-install');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('Download (test)');
  });

  test('openPermissionSettings calls the argument-less bridge method, or reports no bridge', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdater)).toBe('object');
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.openPermissionSettings())).toBe(false);
    const args = await page.evaluate(() => {
      let seen: unknown[] | null = null;
      (window as any).AndroidKiosk = { openInstallPermissionSettings: (...a: unknown[]) => { seen = a; } };
      const ok = (window as any).utAndroidUpdater.openPermissionSettings();
      return { ok, seen };
    });
    expect(args.ok).toBe(true);
    expect(args.seen).toEqual([]);
  });

  test('a click-set "Downloading" label is restored on needs_permission', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await page.evaluate(() => { const f = document.createElement('form'); f.id = 'android-update-form'; f.hidden = true; document.body.appendChild(f); });
    await page.evaluate(() => {
      const b = document.createElement('button');
      b.type = 'button';
      b.setAttribute('data-android-update-install', '');
      b.setAttribute('data-testid', 'android-update-install');
      b.setAttribute('data-orig-label', 'Download (test)');
      b.textContent = 'Downloading update...';
      b.disabled = true;
      document.body.appendChild(b);
    });
    await report(page, 'needs_permission');
    await expect(page.getByTestId('android-update-install')).toHaveText('Download (test)');
  });

  test('permission granted outside the app clears the remembered state', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await page.evaluate(() => { const f = document.createElement('form'); f.id = 'android-update-form'; f.hidden = true; document.body.appendChild(f); });
    await report(page, 'needs_permission');
    await page.evaluate(() => { (window as any).AndroidKiosk = { canInstallPackages: () => true }; });
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.pendingPermission())).toBeNull();
    expect(await page.evaluate((k) => sessionStorage.getItem(k), KEY)).toBeNull();
  });

  test('clearPermission (Not now) forgets the remembered state', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await page.evaluate(() => { const f = document.createElement('form'); f.id = 'android-update-form'; f.hidden = true; document.body.appendChild(f); });
    await report(page, 'needs_permission');
    await page.evaluate(() => (window as any).utAndroidUpdater.clearPermission());
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.pendingPermission())).toBeNull();
  });

  test('with no update form on /settings, needs_permission reloads onto #android-update', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    const nav = page.waitForURL(/\/settings#android-update/);
    await report(page, 'needs_permission');
    await nav;
  });
});
