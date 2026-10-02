import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3354: on iOS before 26, WKWebView's MediaRecorder writes MP4
// only, while the panel (and the stored audio.webm bundle) needs WebM/Ogg.
// The voice button used to ask for the microphone anyway and then fail with
// "Couldn't access the microphone." — now it says up front that this
// device can't record audio, and never calls getUserMedia. Driven here by
// making MediaRecorder.isTypeSupported report MP4-only before the page runs.

test.describe('bug-report voice note — recorder without WebM/Ogg support', () => {
  test('the voice button is disabled with the unsupported text, and the mic is never requested', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(`
      window.__gumCalls = 0;
      if (window.MediaRecorder) {
        MediaRecorder.isTypeSupported = function (t) { return /^audio\\/mp4|^video\\/mp4/.test(t); };
      }
      if (navigator.mediaDevices) {
        navigator.mediaDevices.getUserMedia = function () {
          window.__gumCalls++;
          return Promise.reject(new Error('should not be called'));
        };
      }
    `);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();

    const btn = page.locator('#ir-voice-btn');
    await expect(btn).toBeDisabled();
    await expect(btn).toHaveText("This browser can't record audio.");
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(0);
    assertClean();
  });

  test('a browser with WebM support keeps the voice button live', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    const btn = page.locator('#ir-voice-btn');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('● Record');
    assertClean();
  });
});
