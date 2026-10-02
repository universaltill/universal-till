import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3355: in the iOS app the bug-report panel's screenshot button
// captured only the iOS share prompt — the web capture path has nothing to
// record inside a WKWebView. The iOS shell now registers a native
// `utScreenshot` script-message handler (ios/UniversalTill/ScreenshotBridge.swift:
// WKWebView.takeSnapshot of the till page) and the panel calls it through
// window.webkit.messageHandlers.utScreenshot.postMessage(), which resolves to
// a data:image/png URL, or "" on failure.
//
// The Swift side compiles and runs only on the macOS ios-ci runner, but the
// panel's JS branch can be driven here with a stub handler installed before
// the page's script runs — the same technique as
// android-screenshot-bridge-1435.spec.ts. The stub also records whether the
// panel was hidden at the moment of the capture: on a phone the panel covers
// most of the screen, so the shot must show the till, not the panel.

const TINY_PNG =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=';

// mode: a data URL to resolve with, "" to resolve empty, or "reject".
function installIOSStub(mode: string) {
  return `
    window.__iosShots = [];
    window.webkit = { messageHandlers: { utScreenshot: {
      postMessage: function (msg) {
        var panel = document.getElementById('bugreport-panel');
        window.__iosShots.push({
          msg: msg,
          panelVisibility: panel ? getComputedStyle(panel).visibility : 'missing'
        });
        if (${JSON.stringify(mode)} === 'reject') return Promise.reject(new Error('simulated bridge failure'));
        return Promise.resolve(${JSON.stringify(mode)});
      }
    } } };
  `;
}

async function shots(page: import('@playwright/test').Page): Promise<{ msg: unknown; panelVisibility: string }[]> {
  return page.evaluate(() => (window as any).__iosShots ?? []);
}

test.describe('bug-report panel screenshot → iOS utScreenshot bridge', () => {
  test('a data URL from the bridge becomes a thumbnail, captured with the panel hidden', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(installIOSStub(TINY_PNG));
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    const panel = page.getByTestId('bugreport-panel');
    await expect(panel).toBeVisible();

    const btn = page.locator('#ir-screenshot-btn');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('Take screenshot');

    await btn.click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb img')).toHaveAttribute('src', /^blob:/);
    const calls = await shots(page);
    expect(calls).toHaveLength(1);
    // The panel was out of the way while the native snapshot was taken…
    expect(calls[0].panelVisibility).toBe('hidden');
    // …and is back afterwards, with no error and the button live again.
    await expect(panel).toBeVisible();
    await expect(btn).toBeEnabled();
    await expect(page.locator('#ir-status')).toHaveText('');

    await btn.click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(2);
    expect(await shots(page)).toHaveLength(2);
    assertClean();
  });

  test('"" from the bridge (snapshot failed) reports the error inline and restores the panel', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(installIOSStub(''));
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();

    const btn = page.locator('#ir-screenshot-btn');
    await btn.click();
    await expect(page.locator('#ir-status')).toHaveText("Couldn't capture a screenshot.");
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(0);
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await expect(btn).toBeEnabled();
    expect(await shots(page)).toHaveLength(1);
    assertClean();
  });

  test('a rejected bridge promise is handled the same way, never as an uncaught error', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(installIOSStub('reject'));
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();

    const btn = page.locator('#ir-screenshot-btn');
    await btn.click();
    await expect(page.locator('#ir-status')).toHaveText("Couldn't capture a screenshot.");
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await expect(btn).toBeEnabled();
    assertClean();
  });

  test('without the bridge, window.webkit alone does not divert the button from getDisplayMedia', async ({ page }) => {
    const assertClean = watchConsole(page);
    // A WKWebView host with other handlers (or none) must not be mistaken
    // for the iOS till app: the click must still reach getDisplayMedia.
    await page.addInitScript(`
      window.webkit = { messageHandlers: {} };
      window.__displayMediaCalls = 0;
      navigator.mediaDevices.getDisplayMedia = function () {
        window.__displayMediaCalls++;
        return Promise.reject(new Error('stubbed'));
      };
    `);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    const btn = page.locator('#ir-screenshot-btn');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('Take screenshot');
    await btn.click();
    await expect(page.locator('#ir-status')).toHaveText("Couldn't capture a screenshot.");
    expect(await page.evaluate(() => (window as any).__displayMediaCalls)).toBe(1);
    assertClean();
  });
});
