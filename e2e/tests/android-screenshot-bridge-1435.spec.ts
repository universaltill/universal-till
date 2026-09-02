import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1435: on the Android native shell the bug-report panel's screenshot
// button is backed by window.AndroidKiosk.captureScreenshot() — a synchronous
// native PixelCopy of the WebView added by MainActivity.kt's KioskBridge —
// instead of getDisplayMedia, which Android's WebView simply doesn't have.
// The Kotlin side can't be compiled or run in this repo's test setup (no
// Android SDK, no test source set — see docs/code-reviews/2026-08-29-android-
// kiosk-lock-task.md), but the panel's own JS branching CAN be driven in a
// real browser, same technique as android-kiosk-bridge-1254.spec.ts: install
// a stub for window.AndroidKiosk before the page's script runs, then assert
// the panel wires the button to it and handles both of the bridge's two
// documented outcomes — a data:image/png URL, or "" on failure.
//
// window.AndroidKiosk only ever exists inside the Android shell; every other
// spec in this suite runs this same panel with it undefined, which is what
// keeps the desktop/Pi getDisplayMedia path untouched (asserted last, below).

// A 1x1 transparent PNG — the smallest thing fetch(dataUrl).blob() will turn
// into a real image/png Blob, which is all addScreenshotThumb needs.
const TINY_PNG =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=';

function installKioskStub(returnValue: string) {
  return `
    window.__androidKioskCalls = [];
    window.AndroidKiosk = {
      exitLockdown: function () { window.__androidKioskCalls.push('exitLockdown'); },
      captureScreenshot: function () {
        window.__androidKioskCalls.push('captureScreenshot');
        return ${JSON.stringify(returnValue)};
      }
    };
  `;
}

async function callCount(page: import('@playwright/test').Page): Promise<number> {
  return page.evaluate(() => (window as any).__androidKioskCalls?.length ?? 0);
}

test.describe('bug-report panel screenshot → window.AndroidKiosk.captureScreenshot bridge', () => {
  test('a data URL from the bridge becomes a screenshot thumbnail', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(installKioskStub(TINY_PNG));
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();

    const btn = page.locator('#ir-screenshot-btn');
    // With the bridge present the button is live, never the "not available
    // here" state the card reports.
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('📷 Take screenshot');

    await btn.click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb img')).toHaveAttribute('src', /^blob:/);
    expect(await callCount(page)).toBe(1);
    // Re-enabled for the next shot, and no error surfaced.
    await expect(btn).toBeEnabled();
    await expect(page.locator('#ir-status')).toHaveText('');

    // Several screenshots ride along with one report — same as every other
    // platform's path.
    await btn.click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(2);
    expect(await callCount(page)).toBe(2);

    // The thumbnail's ✕ still removes it (the bridge feeds the SAME
    // addScreenshotThumb every other path uses, so nothing downstream forks).
    await page.locator('#ir-screenshot-thumbs .bugreport-thumb-remove').first().click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);
    assertClean();
  });

  test('"" from the bridge (native capture failed) reports the error inline and re-enables the button', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(installKioskStub(''));
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();

    const btn = page.locator('#ir-screenshot-btn');
    await expect(btn).toBeEnabled();
    await btn.click();
    await expect(page.locator('#ir-status')).toHaveText("Couldn't capture a screenshot.");
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(0);
    await expect(btn).toBeEnabled();
    expect(await callCount(page)).toBe(1);
    assertClean();
  });

  test('a bridge that throws is handled the same way, never as an uncaught error', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(`
      window.AndroidKiosk = {
        exitLockdown: function () {},
        captureScreenshot: function () { throw new Error('simulated bridge failure'); }
      };
    `);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();

    const btn = page.locator('#ir-screenshot-btn');
    await btn.click();
    await expect(page.locator('#ir-status')).toHaveText("Couldn't capture a screenshot.");
    await expect(btn).toBeEnabled();
    assertClean(); // the throw must be caught by the panel, not reach the console
  });
});

test('without the Android bridge the desktop getDisplayMedia path is what the button uses', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  // Headless Chromium has getDisplayMedia, so the button is live via the
  // pre-existing branch — proving the new bridge branch is a strict addition
  // in front of it, not a replacement, and that it is skipped entirely when
  // window.AndroidKiosk is undefined.
  expect(await page.evaluate(() => typeof (window as any).AndroidKiosk)).toBe('undefined');
  const btn = page.locator('#ir-screenshot-btn');
  await expect(btn).toBeEnabled();
  await expect(btn).toHaveText('📷 Take screenshot');
  assertClean();
});
