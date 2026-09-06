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
});
