import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3354: a recorder with none of the voice types the panel can use
// must not ask for the microphone and then fail with "Couldn't access the
// microphone." — it says up front that this device can't record audio, and
// never calls getUserMedia. Driven by making MediaRecorder.isTypeSupported
// report no audio type before the page runs.
//
// ut-docs#3416: iOS before 26 (WKWebView) records MP4 only. The bundle now
// stores that as audio.m4a, so an MP4-only recorder keeps the button live.

test.describe('bug-report voice note — recorder audio support', () => {
  test('with no usable audio type the voice button is disabled with the unsupported text, and the mic is never requested', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(`
      window.__gumCalls = 0;
      if (window.MediaRecorder) {
        MediaRecorder.isTypeSupported = function (t) { return /^video\\//.test(t); };
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

  test('an MP4-only recorder (iOS before 26) keeps the voice button live', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(`
      if (window.MediaRecorder) {
        MediaRecorder.isTypeSupported = function (t) { return /^audio\\/mp4|^video\\/mp4/.test(t); };
      }
    `);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    const btn = page.locator('#ir-voice-btn');
    await expect(btn).toBeEnabled();
    await expect(btn).toHaveText('● Record');
    assertClean();
  });

  test('an MP4 voice note survives a reload mid-recording and is sent as audio.m4a', async ({ page }) => {
    const assertClean = watchConsole(page);
    // A stand-in for WKWebView's MP4-only recorder: emits a chunk per
    // timeslice and a final one on stop, each typed audio/mp4.
    await page.addInitScript(`
      class FakeRecorder extends EventTarget {
        constructor(stream, opts) { super(); this.mimeType = (opts && opts.mimeType) || 'audio/mp4'; this.state = 'inactive'; }
        static isTypeSupported(t) { return /^audio\\/mp4|^video\\/mp4/.test(t); }
        emit() {
          const e = new Event('dataavailable');
          e.data = new Blob([new Uint8Array([0, 0, 0, 24, 102, 116, 121, 112, 77, 52, 65, 32])], { type: this.mimeType });
          this.dispatchEvent(e);
        }
        start(ms) { this.state = 'recording'; this.timer = setInterval(() => this.emit(), ms || 1000); this.emit(); }
        stop() { clearInterval(this.timer); this.state = 'inactive'; this.emit(); this.dispatchEvent(new Event('stop')); }
      }
      window.MediaRecorder = FakeRecorder;
      navigator.mediaDevices.getUserMedia = function () { return Promise.resolve(new MediaStream()); };
    `);
    let posted = '';
    await page.route('**/api/issue-reports', async (route) => {
      posted = route.request().postDataBuffer()?.toString('latin1') ?? '';
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"data":{"id":"test-3416"},"error":null}' });
    });
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    const btn = page.locator('#ir-voice-btn');
    await btn.click();
    await expect(btn).toHaveText(/Stop/);
    await page.waitForTimeout(1500);
    // A full document load while recording (ut-docs#2342): what was captured
    // so far is rebuilt from the draft's chunks on the next page.
    await page.reload();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await expect(page.locator('#ir-voice-preview')).toBeVisible();
    await page.locator('#ir-save-btn').click();
    await expect.poll(() => posted).toContain('name="audio"; filename="audio.m4a"');
    expect(posted).toContain('Content-Type: audio/mp4');
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
