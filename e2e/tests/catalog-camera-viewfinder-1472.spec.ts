import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1472: in-page getUserMedia viewfinder for the catalog photo flow,
// replacing the OS camera round-trip (image-file-camera, #1326) as the
// first choice on any platform that actually supports it. CI's headless
// Chromium has no real camera, so every test here stubs
// `navigator.mediaDevices.getUserMedia` deterministically via an init
// script — same technique, and the same track.stop()-accounting discipline,
// as the sale-screen camera barcode scanner's own spec
// (sale-screen-camera-barcode-scan-548.spec.ts), whose independent review
// found a leaked-stream bug this suite deliberately checks for too.
//
// Uses Butter 250g (itm009, barcode 2000010000098) — untouched by any other
// spec's catalog-image work (catalog-image-to-till.spec.ts uses Sparkling
// Water 500ml and Apple Juice 1L).

// Stubs a MediaStream-backed getUserMedia from an offscreen <canvas> (no real
// camera/permission prompt involved). Every acquired track's stop() is
// counted on window.__stopCalls, and each call increments __gumCalls.
//
// Headless Chromium in this sandbox never actually decodes a captureStream()
// into a <video> element's frame buffer (videoWidth/readyState stay 0
// forever, confirmed while writing this spec — the same class of sandboxed-
// CI media limitation already documented in catalog-image-to-till.spec.ts's
// ut-docs#1362 note for <img> load-state assertions). The app code under
// test doesn't throw on that (drawImage on an unready video draws nothing
// rather than erroring), so rather than depend on this environment's video
// decode pipeline, the video element's dimensions are stubbed directly —
// this exercises the real draw/toBlob/DataTransfer/upload wiring without
// depending on whether the sandbox can render a fake camera feed.
// `videoDims: false` leaves videoWidth/videoHeight at the real (always-0 in
// this sandbox) value, for the "capture before a frame exists" case.
async function stubCamera(page: import('@playwright/test').Page, opts: { videoDims?: boolean } = {}) {
  const videoDims = opts.videoDims !== false;
  await page.addInitScript((videoDims) => {
    (window as any).__gumCalls = 0;
    (window as any).__stopCalls = 0;
    if (videoDims) {
      Object.defineProperty(HTMLVideoElement.prototype, 'videoWidth', { configurable: true, get: () => 64 });
      Object.defineProperty(HTMLVideoElement.prototype, 'videoHeight', { configurable: true, get: () => 48 });
    }
    const canvas = document.createElement('canvas');
    canvas.width = 64;
    canvas.height = 48;
    const ctx = canvas.getContext('2d')!;
    ctx.fillStyle = '#4080c0';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          (window as any).__gumCalls++;
          const stream = (canvas as any).captureStream();
          stream.getTracks().forEach((t: MediaStreamTrack) => {
            const origStop = t.stop.bind(t);
            t.stop = () => { (window as any).__stopCalls++; origStop(); };
          });
          return stream;
        },
      },
    });
  }, videoDims);
}

// Counts calls to the OS-camera fallback input's own .click() — a faster,
// clearer failure than relying solely on `waitForEvent('filechooser')`,
// which times out ambiguously (30s, no error detail) if the fallback never
// fires at all (ut-docs#1472 review finding N3).
async function stubFallbackCounter(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    (window as any).__cameraInputClicks = 0;
    document.addEventListener('DOMContentLoaded', () => {
      const input = document.getElementById('image-file-camera') as HTMLInputElement;
      const orig = input.click.bind(input);
      input.click = () => { (window as any).__cameraInputClicks++; orig(); };
    });
  });
}

async function openItemImagePanel(page: import('@playwright/test').Page) {
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Butter 250g' }).click();
  await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();
}

test.describe('catalog camera viewfinder (ut-docs#1472)', () => {
  test('opens a live preview and Capture uploads the frame via the existing image field', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await openItemImagePanel(page);

    await page.locator('#image-camera-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeVisible();
    await expect(page.locator('#image-viewfinder-video')).toBeVisible();
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    // Wait for the fake stream's metadata so the frame actually has pixels.
    await page.waitForFunction(() => (document.querySelector('#image-viewfinder-video') as HTMLVideoElement).videoWidth > 0);

    await page.locator('#image-viewfinder-capture-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeHidden();
    await expect(page.locator('#image-file-name')).toHaveText('catalog-photo.jpg');
    // The stream must actually be released, not left recording behind the
    // now-hidden viewfinder.
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    // The captured frame is a real Blob wired into the SAME canonical
    // #image-file field the plain file picker and the OS-camera input both
    // use — prove it actually reaches the server via the existing form.
    await page.locator('#image-form button[type=submit]').click();
    await expect(page.locator('#image-msg')).toContainText('updated');

    assertClean();
  });

  test('Cancel releases the camera without touching the file field', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await openItemImagePanel(page);

    await page.locator('#image-camera-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeVisible();
    await page.waitForFunction(() => (document.querySelector('#image-viewfinder-video') as HTMLVideoElement).videoWidth > 0);

    await page.locator('#image-viewfinder-cancel-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeHidden();
    await expect(page.locator('#image-file-name')).toHaveText('');
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });

  test('a getUserMedia rejection (no camera / permission refused) falls back to the file picker, viewfinder never shown', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubFallbackCounter(page);
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: { getUserMedia: async () => { throw new Error('NotFoundError'); } },
      });
    });
    await openItemImagePanel(page);

    const [chooser] = await Promise.all([
      page.waitForEvent('filechooser'),
      page.locator('#image-camera-btn').click(),
    ]);
    // Fast, unambiguous check too (ut-docs#1472 review N3) — if the
    // fallback path ever regresses, this fails as "expected 1, received 0"
    // rather than only a bare 30s filechooser timeout with no clue why.
    expect(await page.evaluate(() => (window as any).__cameraInputClicks)).toBe(1);
    expect(chooser.isMultiple()).toBe(false);
    await expect(page.locator('#image-viewfinder')).toBeHidden();

    assertClean();
  });

  // ut-docs#1251 precedent (sale-screen-camera-barcode-scan-548.spec.ts): on
  // a non-secure-context origin `navigator.mediaDevices` is undefined
  // entirely, and calling `.getUserMedia` on it throws a SYNCHRONOUS
  // TypeError, bypassing any .catch(). The click handler here gates on
  // `navigator.mediaDevices` truthiness BEFORE ever touching
  // `.getUserMedia`, so this must fall back cleanly with no uncaught error.
  test('falls back without throwing when navigator.mediaDevices is undefined entirely', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubFallbackCounter(page);
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: undefined });
    });
    await openItemImagePanel(page);

    const [chooser] = await Promise.all([
      page.waitForEvent('filechooser'),
      page.locator('#image-camera-btn').click(),
    ]);
    expect(await page.evaluate(() => (window as any).__cameraInputClicks)).toBe(1);
    expect(chooser.isMultiple()).toBe(false);
    await expect(page.locator('#image-viewfinder')).toBeHidden();

    assertClean();
  });

  // Reproduces the double-open race the sale-screen camera scanner's review
  // (#548) found in the same shape: a second click before the first request
  // resolves must not fire a second getUserMedia call (which would acquire
  // and then immediately orphan a stream nothing ever stops).
  test('re-clicking Take a Photo while a request is already in flight does not open a second stream', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).__gumCalls = 0;
      (window as any).__resolveGum = null;
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: {
          getUserMedia: () => {
            (window as any).__gumCalls++;
            return new Promise((resolve) => {
              (window as any).__resolveGum = () => {
                const canvas = document.createElement('canvas');
                canvas.width = 64;
                canvas.height = 48;
                resolve((canvas as any).captureStream());
              };
            });
          },
        },
      });
    });
    await openItemImagePanel(page);

    const cameraBtn = page.locator('#image-camera-btn');
    await cameraBtn.click();
    // Still pending — the viewfinder stays hidden until getUserMedia resolves.
    await expect(page.locator('#image-viewfinder')).toBeHidden();
    await cameraBtn.click();
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    await page.evaluate(() => (window as any).__resolveGum());
    await expect(page.locator('#image-viewfinder')).toBeVisible();

    assertClean();
  });

  // Same race, but the second click happens once the feed is already live —
  // clicking Take a Photo again while it's showing must be a no-op, not a
  // second overlapping stream.
  test('re-clicking Take a Photo while the viewfinder is already open does not open a second stream', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await openItemImagePanel(page);

    const cameraBtn = page.locator('#image-camera-btn');
    await cameraBtn.click();
    await expect(page.locator('#image-viewfinder')).toBeVisible();
    await cameraBtn.click();
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    await page.locator('#image-viewfinder-cancel-btn').click();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });

  test('desktop behaviour with no getUserMedia support at all is unchanged (feature-detect, not platform sniffing)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubFallbackCounter(page);
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: {} });
    });
    await openItemImagePanel(page);

    const [chooser] = await Promise.all([
      page.waitForEvent('filechooser'),
      page.locator('#image-camera-btn').click(),
    ]);
    expect(await page.evaluate(() => (window as any).__cameraInputClicks)).toBe(1);
    expect(chooser.isMultiple()).toBe(false);
    await expect(page.locator('#image-viewfinder')).toBeHidden();

    assertClean();
  });

  // ut-docs#1472 review B2 (blocker): collapsing the "Item image" details
  // panel while the viewfinder is open must release the camera — otherwise
  // the stream (and the OS's own recording indicator) stays live with no
  // visible UI anywhere pointing at it.
  test('collapsing the Item image panel while the viewfinder is open releases the camera', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await openItemImagePanel(page);

    await page.locator('#image-camera-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeVisible();
    expect(await page.evaluate(() => (window as any).__stopCalls)).toBe(0);

    // Click the SAME summary that opened the panel, collapsing it.
    await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });

  // ut-docs#1472 review S2 (should-fix): getUserMedia resolving is not the
  // same as a decoded frame existing yet. Capturing in that window must
  // report an inline error and leave the viewfinder open for a retry, not
  // vanish silently with no photo and no explanation.
  test('capturing before the video has a real frame reports an error and keeps the viewfinder open', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page, { videoDims: false }); // videoWidth/Height stay 0
    await openItemImagePanel(page);

    await page.locator('#image-camera-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeVisible();

    await page.locator('#image-viewfinder-capture-btn').click();
    await expect(page.locator('#image-viewfinder')).toBeVisible(); // still open
    await expect(page.locator('#image-viewfinder-msg')).not.toHaveText('');
    await expect(page.locator('#image-file-name')).toHaveText('');
    expect(await page.evaluate(() => (window as any).__stopCalls)).toBe(0); // camera still running

    // Cleans up: closing the panel still releases the camera afterwards.
    await page.locator('details.catalog-extra', { hasText: 'Item image' }).locator('summary').click();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });
});
