import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#548: camera barcode/QR scan as an alternative input mode on the
// cashier sale screen, alongside the existing wedge/HID scanner path
// (ut-docs#76/#423). Decoding is 100% client-side via the browser's native
// BarcodeDetector — CI's headless Chromium doesn't reliably ship it (and
// never has a real camera), so every test here stubs both `BarcodeDetector`
// and `getUserMedia` deterministically via an init script rather than
// depending on the runner's actual capabilities.
const BARCODE = '2000010000012'; // Coca-Cola 330ml (internal/db/migrations/001_init.sql)

// Stubs a MediaStream-backed getUserMedia (from an offscreen <canvas>, so no
// real camera/permission prompt is ever involved) and a BarcodeDetector whose
// `detect()` result is controlled by `window.__scanResult`. Every acquired
// track's `stop()` is counted on `window.__stopCalls` — the independent
// review (ut-docs#548) found the camera was never actually observed to be
// released by any test, which is exactly the class of bug (orphaned live
// stream behind a closed overlay) that was hiding.
async function stubCamera(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    (window as any).__scanCalls = 0;
    (window as any).__gumCalls = 0;
    (window as any).__stopCalls = 0;
    (window as any).BarcodeDetector = class {
      async detect() {
        (window as any).__scanCalls++;
        const result = (window as any).__scanResult;
        return result ? [{ rawValue: result }] : [];
      }
    };
    const canvas = document.createElement('canvas');
    canvas.width = 10;
    canvas.height = 10;
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
  });
}

test.describe('camera barcode scan on the sale screen (ut-docs#548)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('hidden entirely when the browser has no BarcodeDetector', async ({ page }) => {
    const assertClean = watchConsole(page);
    // No stub at all here — app.js's own typeof-check must find nothing.
    await page.addInitScript(() => {
      // Guarantee determinism regardless of what the runner's real Chromium
      // ships: force the unsupported branch app.js is meant to take.
      Object.defineProperty(window, 'BarcodeDetector', { value: undefined, configurable: true });
    });
    await page.goto('/');
    await expect(page.locator('#barcode-scan-open')).toBeHidden();
    assertClean();
  });

  test('scanning a code rings up the item and closes the overlay, without touching the wedge-scan path', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/');
    await page.evaluate((code) => { (window as any).__scanResult = code; }, BARCODE);

    const openBtn = page.locator('#barcode-scan-open');
    await expect(openBtn).toBeVisible();

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      openBtn.click(),
    ]);

    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
    // The stream must actually be released once a match is found — not just
    // the overlay hidden with the camera still recording behind it.
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    // Regression check (same class of bug as ut-docs#423): the wedge-scanner
    // keydown path must still work after the camera overlay has opened and
    // closed once — nothing it does may detach or shadow the global listener.
    const codeInput = page.locator('form.scan-row input[name="code"]');
    await codeInput.click();
    // Butter 250g (itm009): 001_init.sql seeds '...093', corrected to the
    // real EAN-13 check digit '...098' by migration 031 (ut-docs#191).
    await page.keyboard.type('2000010000098', { delay: 5 });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#basket')).toContainText('Butter 250g');

    assertClean();
  });

  test('closing without a match stops the camera and leaves the sale screen untouched', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/');
    // __scanResult left unset: detect() always resolves empty, so the
    // overlay would stay open scanning forever until the cashier cancels.

    const openBtn = page.locator('#barcode-scan-open');
    await expect(openBtn).toBeVisible();
    await openBtn.click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect.poll(() => page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    await page.locator('#barcode-scan-close').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola 330ml');
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });

  test('a camera error surfaces inline instead of a stuck or silent overlay', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).BarcodeDetector = class {
        async detect() { return []; }
      };
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: { getUserMedia: async () => { throw new Error('denied'); } },
      });
    });
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-status')).toHaveText('Camera unavailable');
    await page.locator('#barcode-scan-close').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();

    assertClean();
  });

  // ut-docs#1251: on a non-secure-context origin (plain http:// to a LAN IP
  // rather than localhost) `navigator.mediaDevices` is undefined entirely,
  // and calling `.getUserMedia` on it throws a SYNCHRONOUS TypeError before
  // the promise chain (and its .catch()) even exists — an uncaught
  // exception, not the declared "Camera unavailable" status. Confirmed
  // against a real Chromium instance during independent review (secure
  // context vs. plain-http origin) before landing this test.
  test('reports the existing camera-unavailable status instead of throwing when mediaDevices is unavailable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).BarcodeDetector = class {
        async detect() { return []; }
      };
      // Simulates a non-secure-context origin, where the platform never
      // defines navigator.mediaDevices at all.
      Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: undefined });
    });
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect(page.locator('#barcode-scan-status')).toHaveText('Camera unavailable');
    await page.locator('#barcode-scan-close').click();

    // The whole point: no uncaught pageerror from the guard-less call.
    assertClean();
  });

  // The three races below reproduce what the independent review (ut-docs#548)
  // found live against the real page: neither async continuation in the
  // camera IIFE re-checked whether the cashier had already closed the
  // overlay, which could leave a camera recording behind a hidden overlay
  // or ring up a line the cashier can no longer see. Each stubs the async
  // boundary as a manually-resolved Promise so the race is deterministic
  // (no reliance on real timing).

  test('closing while the camera is still starting releases it once it arrives, without scanning', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).__stopCalls = 0;
      (window as any).__scanCalls = 0;
      (window as any).BarcodeDetector = class {
        async detect() { (window as any).__scanCalls++; return [{ rawValue: '2000010000012' }]; }
      };
      const canvas = document.createElement('canvas');
      canvas.width = 10;
      canvas.height = 10;
      (window as any).__resolveGum = null;
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: {
          // Never resolves until the test explicitly calls __resolveGum(),
          // simulating a slow first-use permission prompt / camera start.
          getUserMedia: () => new Promise((resolve) => {
            (window as any).__resolveGum = () => {
              const stream = (canvas as any).captureStream();
              stream.getTracks().forEach((t: MediaStreamTrack) => {
                const origStop = t.stop.bind(t);
                t.stop = () => { (window as any).__stopCalls++; origStop(); };
              });
              resolve(stream);
            };
          }),
        },
      });
    });
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    // Cashier closes before the camera ever actually starts.
    await page.locator('#barcode-scan-close').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();

    // The slow getUserMedia now resolves, after the overlay is already closed.
    await page.evaluate(() => (window as any).__resolveGum());
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);
    // Never should have started decoding, let alone rung anything up.
    expect(await page.evaluate(() => (window as any).__scanCalls)).toBe(0);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola 330ml');

    assertClean();
  });

  test('an in-flight decode that resolves after Close does not ring up a line', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).__stopCalls = 0;
      (window as any).__resolveDetect = null;
      (window as any).BarcodeDetector = class {
        detect() {
          // Only the FIRST detect() call is held open by the test; if the
          // fix under review regresses, a second frame could also fire
          // before Close and would resolve immediately (harmless either way
          // since the assertion below only cares whether a line was rung up).
          if (!(window as any).__resolveDetect) {
            return new Promise((resolve) => {
              (window as any).__resolveDetect = () => resolve([{ rawValue: '2000010000012' }]);
            });
          }
          return Promise.resolve([]);
        }
      };
      const canvas = document.createElement('canvas');
      canvas.width = 10;
      canvas.height = 10;
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: {
          getUserMedia: async () => {
            const stream = (canvas as any).captureStream();
            stream.getTracks().forEach((t: MediaStreamTrack) => {
              const origStop = t.stop.bind(t);
              t.stop = () => { (window as any).__stopCalls++; origStop(); };
            });
            return stream;
          },
        },
      });
    });
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    // Wait until the camera IIFE has actually called detect() and is holding
    // it open, so the resolve below lands strictly after Close.
    await expect.poll(() => page.evaluate(() => !!(window as any).__resolveDetect)).toBe(true);

    await page.locator('#barcode-scan-close').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();

    // The barcode "arrives" only now, after the cashier already closed.
    await page.evaluate(() => (window as any).__resolveDetect());
    // Give the resolved promise's .then a turn, then assert nothing rang up.
    await page.waitForTimeout(100);
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola 330ml');

    assertClean();
  });

  test('re-triggering open (e.g. Enter on a focused, already-open button) does not leak a second stream', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/');

    const openBtn = page.locator('#barcode-scan-open');
    await openBtn.click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect.poll(() => page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    // The button keeps focus after the click that opened the overlay; a
    // wedge scanner's own keydown buffer (app.js) doesn't preventDefault a
    // bare Enter, so it can reach the still-focused button and re-fire it.
    await page.keyboard.press('Enter');
    // Give any (incorrect) second open() a turn to call getUserMedia again.
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);

    await page.locator('#barcode-scan-close').click();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });
});
