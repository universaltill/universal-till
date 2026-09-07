import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1292: the camera overlay's getUserMedia .catch() collapsed every
// rejection into one generic "Camera unavailable" message. Now it branches
// on err.name so a cashier gets an actionable, distinct message for
// permission-denied vs no-camera vs camera-busy. Each test below stubs
// getUserMedia to reject with a DOMException of the matching name and
// asserts the overlay's status shows the right translated string.

// Stub getUserMedia to reject with a DOMException of the given name/message.
// BarcodeDetector is stubbed (headless Chromium doesn't ship it reliably) so
// the barcode-scan overlay is reachable without a real camera.
async function stubCameraReject(
  page: import('@playwright/test').Page,
  name: string,
  message: string,
) {
  await page.addInitScript(({ name, message }) => {
    (window as any).BarcodeDetector = class {
      async detect() {
        return [];
      }
    };
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

test.describe('camera error branching on err.name (ut-docs#1292)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('NotFoundError shows the no-camera message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotFoundError', 'no camera');
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect(page.locator('#barcode-scan-status')).toHaveText(
      'No camera found on this device.',
    );

    assertClean();
  });

  test('NotAllowedError shows the permission-denied message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotAllowedError', 'permission denied');
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect(page.locator('#barcode-scan-status')).toHaveText(
      'Camera permission was denied. Please allow camera access in your browser settings.',
    );

    assertClean();
  });

  test('NotReadableError shows the camera-busy message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'NotReadableError', 'in use');
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect(page.locator('#barcode-scan-status')).toHaveText(
      'Camera is in use by another app. Close the other app and try again.',
    );

    assertClean();
  });

  test('unknown err.name falls back to the generic camera_error message', async ({ page }) => {
    const assertClean = watchConsole(page);
    await stubCameraReject(page, 'SomeOtherError', 'unknown');
    await page.goto('/');

    await page.locator('#barcode-scan-open').click();
    await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
    await expect(page.locator('#barcode-scan-status')).toHaveText(
      'Camera unavailable',
    );

    assertClean();
  });
});
