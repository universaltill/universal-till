import { test, expect } from './fixtures';
import { gotoTillsSection } from './helpers';

// ut-docs#3428, WebKit phone lane (iOS WKWebView has no native
// BarcodeDetector, so the vendored zxing-wasm decoder is what runs): the REAL
// decoder reads a REAL QR picture. The picture is the one the till's own
// "Show pairing code" endpoint renders (Go qrcode of encodeEnrollCode), not a
// hand-made fixture. Only the camera is faked: a canvas stream showing it.

test.use({ viewport: { width: 390, height: 844 } });

test('the vendored decoder reads the main till QR and fills the join form', async ({ page, request, baseURL }) => {
  const origin = new URL(baseURL!).origin;
  const res = await request.post('/api/sync/enroll-token', { form: { url: origin } });
  expect(res.status()).toBe(200);
  const html = await res.text();
  const png = /<img[^>]*src="(data:image\/png;base64,[^"]+)"/.exec(html)?.[1];
  const longCode = /<code style="user-select:all">([^<]+)<\/code>/.exec(html)?.[1];
  expect(png, 'QR image in the pairing panel').toBeTruthy();
  expect(longCode, 'long code in the pairing panel').toBeTruthy();

  await page.addInitScript((src) => {
    // No BarcodeDetector: force the vendored decoder path, as on iOS.
    (window as any).BarcodeDetector = undefined;
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          const img = new Image();
          img.src = src;
          await img.decode();
          const c = document.createElement('canvas');
          c.width = 480; c.height = 480;
          const g = c.getContext('2d')!;
          const draw = () => {
            g.fillStyle = '#fff'; g.fillRect(0, 0, 480, 480);
            g.drawImage(img, 130, 130, 220, 220);
          };
          draw();
          setInterval(draw, 100);
          return c.captureStream(10);
        },
      },
    });
  }, png);

  await gotoTillsSection(page, 'add', '?need=join');
  await page.locator('#tills-tab-code').click();
  await page.locator('#tills-join-scan').click();
  await expect(page.locator('#tills-join-scanned')).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('form[hx-post="/api/sync/join"] input[name=code]')).toHaveValue(longCode!);
});
