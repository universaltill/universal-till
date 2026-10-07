import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1559: sibling gap to camera-error-branching-1292.spec.ts, found
// during independent review of universal-till#772 (ut-docs#1292's fix).
// That spec only drives the scan.camera (barcode-scan) overlay
// (#barcode-scan-open/#barcode-scan-status); the ai.identify overlay
// (web/public/app.js) duplicates the same err.name-branching logic in its
// own IIFE, with zero regression coverage of its own. A future edit that
// touches one overlay's data-msg-* attribute and not the other's would
// ship a silent blank/wrong error message on this overlay and nothing in
// CI would catch it (guard-i18n.sh still passes — the key exists, just
// under the wrong attribute — and the scan.camera e2e test drives a
// different overlay entirely).
//
// Runs against the dedicated 'ai-identify' Playwright project/server
// (see playwright.config.ts + run-till-ai.sh): unlike barcode-scan, the
// ai-identify button/overlay markup doesn't exist in the DOM at all
// unless the server resolves `.aiIdentify` true (`{{ if .aiIdentify }}`
// in web/ui/pages/index.html, gated on UT_AI_ENDPOINT/UT_AI_API_KEY), so
// it can't join the shared default-project till the way the barcode-scan
// overlay's own tests do.

async function stubCameraReject(
  page: import('@playwright/test').Page,
  name: string,
  message: string,
) {
  await page.addInitScript(({ name, message }) => {
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          throw new DOMException(message, name);
        },
      },
    });
  }, { name, message });
}

test.describe('ai.identify camera error branching on err.name (ut-docs#1559)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('NotFoundError shows the no-camera message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotFoundError', 'no camera');
    await page.goto('/');

    await page.locator('#ai-identify-open').click();
    await expect(page.locator('#ai-identify-overlay')).toBeVisible();
    await expect(page.locator('#ai-identify-status')).toHaveText(
      'No camera found on this device.',
    );

    assertClean();
  });

  test('NotAllowedError shows the permission-denied message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotAllowedError', 'permission denied');
    await page.goto('/');

    await page.locator('#ai-identify-open').click();
    await expect(page.locator('#ai-identify-overlay')).toBeVisible();
    await expect(page.locator('#ai-identify-status')).toHaveText(
      'Camera permission was denied. Please allow camera access in your browser settings.',
    );

    assertClean();
  });

  test('NotReadableError shows the camera-busy message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotReadableError', 'in use');
    await page.goto('/');

    await page.locator('#ai-identify-open').click();
    await expect(page.locator('#ai-identify-overlay')).toBeVisible();
    await expect(page.locator('#ai-identify-status')).toHaveText(
      'Camera is in use by another app. Close the other app and try again.',
    );

    assertClean();
  });

  test('unknown err.name falls back to the generic camera_error message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'SomeOtherError', 'unknown');
    await page.goto('/');

    await page.locator('#ai-identify-open').click();
    await expect(page.locator('#ai-identify-overlay')).toBeVisible();
    await expect(page.locator('#ai-identify-status')).toHaveText(
      'Camera unavailable',
    );

    assertClean();
  });

  // ut-docs#3807: Sell -> Menu -> Sell swaps #ut-page (ADR-0098); the
  // returning sell page's server-hidden button must be shown and wired again.
  test('the button survives Sell -> Menu -> Sell and still opens the camera (ut-docs#3807)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotFoundError', 'Requested device not found');
    await page.goto('/');
    await expect(page.locator('#ai-identify-open')).toBeVisible();
    await page.getByTestId('nav-menu').click();
    await expect(page).toHaveURL(/\/menu$/);
    await page.getByTestId('nav-till').click();
    await expect(page).toHaveURL(/\/$/);

    await page.locator('#ai-identify-open').click();
    await expect(page.locator('#ai-identify-overlay')).toBeVisible();
    await expect(page.locator('#ai-identify-status')).toHaveText(
      'No camera found on this device.',
    );
    assertClean();
  });

  // ut-docs#3807 review: the camera can arrive after the cashier has left
  // (permission prompt answered late). It must be stopped, not kept alive in
  // the previous page's detached overlay.
  test('a camera granted after leaving Sell is released (ut-docs#3807)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(() => {
      (window as any).__stopCalls = 0;
      const canvas = document.createElement('canvas');
      canvas.width = 10;
      canvas.height = 10;
      Object.defineProperty(navigator, 'mediaDevices', {
        configurable: true,
        value: {
          getUserMedia: () => new Promise((resolve) => {
            (window as any).__grant = () => {
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
    await page.locator('#ai-identify-open').click();
    await expect.poll(() => page.evaluate(() => typeof (window as any).__grant)).toBe('function');
    // The overlay covers the rail; a programmatic click is the same shell nav.
    await page.evaluate(() => (document.querySelector('[data-testid="nav-menu"]') as HTMLElement).click());
    await expect(page).toHaveURL(/\/menu$/);
    await page.evaluate(() => (window as any).__grant());
    await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBe(1);
    assertClean();
  });
});
