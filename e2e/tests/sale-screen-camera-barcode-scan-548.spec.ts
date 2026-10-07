import { test, expect } from './fixtures';
import { scanAtScannerSpeed, watchConsole } from './helpers';

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

// ut-docs#696: a camera stub whose frames show a real EAN-13 (drawn from the
// standard L/G/R module tables), with BarcodeDetector forced absent so app.js
// must fall back to the vendored decoder. The canvas is repainted every frame
// so captureStream() keeps delivering frames to the <video>.
async function stubCameraShowingEAN13(page: import('@playwright/test').Page, code: string) {
  await page.addInitScript((code: string) => {
    Object.defineProperty(window, 'BarcodeDetector', { value: undefined, configurable: true });
    (window as any).__stopCalls = 0;
    const L = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011'];
    const R = L.map((p) => p.replace(/./g, (b) => (b === '0' ? '1' : '0')));
    const G = R.map((p) => p.split('').reverse().join(''));
    const PARITY = ['LLLLLL', 'LLGLGG', 'LLGGLG', 'LLGGGL', 'LGLLGG', 'LGGLLG', 'LGGGLL', 'LGLGLG', 'LGLGGL', 'LGGLGL'];
    const d = code.split('').map(Number);
    let bits = '101';
    for (let i = 1; i <= 6; i++) bits += (PARITY[d[0]][i - 1] === 'L' ? L : G)[d[i]];
    bits += '01010';
    for (let i = 7; i <= 12; i++) bits += R[d[i]];
    bits += '101';
    const module = 4;
    const quiet = 12 * module;
    const canvas = document.createElement('canvas');
    canvas.width = bits.length * module + 2 * quiet;
    canvas.height = 240;
    const ctx = canvas.getContext('2d')!;
    const paint = () => {
      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      ctx.fillStyle = '#000';
      for (let i = 0; i < bits.length; i++) if (bits[i] === '1') ctx.fillRect(quiet + i * module, 20, module, 200);
      requestAnimationFrame(paint);
    };
    paint();
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          const stream = (canvas as any).captureStream(15);
          stream.getTracks().forEach((t: MediaStreamTrack) => {
            const origStop = t.stop.bind(t);
            t.stop = () => { (window as any).__stopCalls++; origStop(); };
          });
          return stream;
        },
      },
    });
  }, code);
}

test.describe('camera barcode scan on the sale screen (ut-docs#548)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  // ut-docs#696: WebKit (every iPhone/iPad, incl. the iOS app's WKWebView)
  // and some Android WebViews have no BarcodeDetector. The button must still
  // show, and the vendored zxing-wasm decoder (web/public/vendor/
  // barcode-detector/) decodes for real — no stubbed detector here: the
  // stubbed camera shows a genuine EAN-13 drawn on a canvas.
  test('without BarcodeDetector the vendored decoder reads a real barcode, loaded lazily and only from this till (ut-docs#696)', async ({ page, baseURL }) => {
    const assertClean = watchConsole(page);
    const requests: string[] = [];
    page.on('request', (r) => requests.push(r.url()));
    await stubCameraShowingEAN13(page, BARCODE);
    await page.goto('/');

    const openBtn = page.locator('#barcode-scan-open');
    await expect(openBtn).toBeVisible();
    // Lazy: a till that never taps the button never fetches the ~1 MB decoder.
    expect(requests.filter((u) => u.includes('/vendor/barcode-detector/'))).toEqual([]);

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan'), { timeout: 20_000 }),
      openBtn.click(),
    ]);

    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    const origin = new URL(baseURL ?? page.url()).origin;
    // Content-versioned (?v=) so the ~1 MB decoder is cached, not refetched
    // on every page load (no-cache + no validators on the embed FS).
    const decoderURLs = requests.filter((u) => u.includes('/public/vendor/barcode-detector/'));
    expect(decoderURLs.some((u) => /\/ponyfill\.js\?v=\w+/.test(u))).toBe(true);
    expect(decoderURLs.some((u) => /\/zxing_reader\.wasm\?v=\w+/.test(u))).toBe(true);
    const wasmResp = await page.request.get(decoderURLs.find((u) => u.includes('zxing_reader.wasm'))!);
    expect(wasmResp.headers()['content-type']).toBe('application/wasm');
    expect(wasmResp.headers()['cache-control']).toContain('immutable');
    // Frames and the decoder never involve another host (no CDN fallback).
    expect(requests.filter((u) => /^https?:/.test(u) && new URL(u).origin !== origin)).toEqual([]);
    assertClean();
  });

  test('without BarcodeDetector, a decoder that fails to load says so instead of scanning silently forever (ut-docs#696)', async ({ page }) => {
    // Emscripten logs its own failed-instantiation messages; those are the
    // expected symptom here, any other console error still fails the test.
    const assertClean = watchConsole(page, /wasm|ArrayBuffer instantiation|Aborted\(/i);
    await stubCameraShowingEAN13(page, BARCODE);
    const wasmRoute = '**/public/vendor/barcode-detector/zxing_reader.wasm*';
    await page.route(wasmRoute, (route) => route.fulfill({ status: 404, body: 'gone' }));
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-status')).toHaveText('Camera unavailable', { timeout: 20_000 });
    // The camera is not left running behind the error.
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);
    await page.locator('#barcode-scan-close').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola 330ml');

    // The failure is not cached: once the file is reachable again, the next
    // tap loads the decoder and scans.
    await page.unroute(wasmRoute);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan'), { timeout: 20_000 }),
      page.locator('#barcode-scan-open').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');

    assertClean();
  });

  // A BarcodeDetector that exists but whose platform service is missing
  // (Chromium on Android without Play services): getSupportedFormats() is
  // empty and detect() always rejects. The vendored decoder must take over.
  test('a native BarcodeDetector that supports none of our formats falls back to the vendored decoder (ut-docs#696)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraShowingEAN13(page, BARCODE);
    await page.addInitScript(() => {
      (window as any).__nativeDetectCalls = 0;
      // defineProperty: the camera stub above made the property read-only.
      Object.defineProperty(window, 'BarcodeDetector', { configurable: true, value: class {
        static async getSupportedFormats() { return []; }
        async detect() {
          (window as any).__nativeDetectCalls++;
          throw new DOMException('Barcode detection service unavailable.', 'NotSupportedError');
        }
      } });
    });
    await page.goto('/');

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan'), { timeout: 20_000 }),
      page.locator('#barcode-scan-open').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');
    expect(await page.evaluate(() => (window as any).__nativeDetectCalls)).toBe(0);

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
    // ut-docs#2345: scanAtScannerSpeed (see helpers.ts) measures the actual
    // keydown gaps and redoes an attempt that fell below wedge-scanner speed
    // under a loaded (parallel-workers) host, rather than asserting an
    // outcome a slow CDP round trip never earned — same fix already applied
    // to ut-docs#423's own spec for the identical pattern.
    const codeInput = page.locator('form.scan-row input[name="code"]');
    // Butter 250g (itm009): 001_init.sql seeds '...093', corrected to the
    // real EAN-13 check digit '...098' by migration 031 (ut-docs#191).
    await scanAtScannerSpeed(page, '2000010000098', async () => {
      await codeInput.click();
    });
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

  // ut-docs#3807: the product owner went Sell -> Menu -> Sell and the scan
  // button was gone. A menu link is a boosted #ut-page swap (ADR-0098), so
  // the sell page's fresh, server-hidden button needs binding again; app.js
  // only ran once, at the first document load.
  test('Sell -> Menu -> Sell keeps the scan button, wired once (ut-docs#3807)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/');
    await expect(page.locator('#barcode-scan-open')).toBeVisible();

    for (let i = 0; i < 2; i++) {
      await page.getByTestId('nav-menu').click();
      await expect(page).toHaveURL(/\/menu$/);
      await page.getByTestId('nav-till').click();
      await expect(page).toHaveURL(/\/$/);
    }

    const openBtn = page.locator('#barcode-scan-open');
    await expect(openBtn).toBeVisible();
    await openBtn.click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    // One tap, one camera: re-binding never stacks listeners.
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);
    await page.locator('#barcode-scan-close').click();
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);

    assertClean();
  });

  test('a till first opened on another page still gets the scan button on Sell (ut-docs#3807)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/menu');
    await page.getByTestId('nav-till').click();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.locator('#barcode-scan-open')).toBeVisible();
    assertClean();
  });

  test('leaving Sell with the scanner open releases the camera (ut-docs#3807)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCamera(page);
    await page.goto('/');
    await page.locator('#barcode-scan-open').click();
    await expect.poll(() => page.evaluate(() => (window as any).__gumCalls)).toBe(1);
    await expect.poll(() => page.evaluate(() => !!document.querySelector<HTMLVideoElement>('#barcode-scan-video')?.srcObject)).toBe(true);
    // The overlay covers the rail; a programmatic click is the shell nav
    // an idle-lock, a hardware Back or a deep link would also trigger.
    await page.evaluate(() => (document.querySelector('[data-testid="nav-menu"]') as HTMLElement).click());
    await expect(page).toHaveURL(/\/menu$/);
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);
    assertClean();
  });
});
