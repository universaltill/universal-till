import { test, expect } from './fixtures';

// ut-docs#4495: on a real tablet the update button said "Opening Android's
// installer…", no installer came up, and the button stayed disabled with
// that text until the operator left Settings. Android's installer is a
// see-through dialog activity, so the WebView behind it never goes hidden
// and the old `visibilitychange`-only recovery never ran.
//
// web/public/android-update.js now treats a `closed` report as "tap to open
// it again" — sent by MainActivity.onResume after it launched the installer,
// or synthesised by the page when `installing` gets no answer within its
// grace period. This drives the shared [data-android-update-install] button
// (the check-for-updates result, update_api.go) the way the native shell
// does: through window.utAndroidUpdate. The e2e till is not an Android
// till, so the button is planted with the same data-* attributes the server
// renders; the script under test is the real one the page loads.
const AGAIN = 'Tap to open it again (test)';
const INSTALLING = 'Opening installer (test)';
const RETRY = 'Failed, retry (test)';

async function plantButton(page: import('@playwright/test').Page) {
  await page.evaluate(
    ({ again, installing, retry }) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.setAttribute('data-android-update-install', '');
      b.setAttribute('data-testid', 'android-update-install');
      b.setAttribute('data-installing', installing);
      b.setAttribute('data-retry', retry);
      b.setAttribute('data-again', again);
      b.textContent = 'Download';
      document.body.appendChild(b);
    },
    { again: AGAIN, installing: INSTALLING, retry: RETRY },
  );
}

function report(page: import('@playwright/test').Page, state: string) {
  return page.evaluate((s) => (window as any).utAndroidUpdate({ state: s, percent: s === 'installing' ? 100 : null, error: null }), state);
}

test.describe('android-update.js: installer closed → tap to open it again (ut-docs#4495)', () => {
  test('a "closed" report re-enables the button and says it can open the installer again', async ({ page }) => {
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await plantButton(page);
    const btn = page.getByTestId('android-update-install');

    await report(page, 'installing');
    await expect(btn).toBeDisabled();
    await expect(btn).toHaveText(INSTALLING);

    await report(page, 'closed');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText(AGAIN);
  });

  test('"installing" with no answer turns into "closed" after the grace period, not before', async ({ page }) => {
    await page.clock.install();
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await plantButton(page);
    const btn = page.getByTestId('android-update-install');

    await report(page, 'installing');
    await page.clock.runFor(10000);
    await expect(btn).toBeDisabled();
    await expect(btn).toHaveText(INSTALLING);

    await page.clock.runFor(4000);
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText(AGAIN);
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.state().state)).toBe('closed');
  });

  test('a later report cancels the grace timer (a failure stays a failure)', async ({ page }) => {
    await page.clock.install();
    await page.goto('/settings');
    await expect.poll(() => page.evaluate(() => typeof (window as any).utAndroidUpdate)).toBe('function');
    await plantButton(page);
    const btn = page.getByTestId('android-update-install');

    await report(page, 'installing');
    await report(page, 'failed');
    await page.clock.runFor(20000);
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText(RETRY);
    expect(await page.evaluate(() => (window as any).utAndroidUpdater.state().state)).toBe('failed');
  });
});
