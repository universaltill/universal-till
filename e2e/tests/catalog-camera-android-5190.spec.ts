import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';

// ut-docs#5190: inside the Android app (its shell injects window.AndroidKiosk)
// "Take a photo" opens the SYSTEM camera, like iPhone/iPad (#4096), not the
// in-page getUserMedia viewfinder of #1472. The shell's file chooser launches
// the camera for the capture input, so the page's job is only to click that
// input. Desktop browsers keep the viewfinder
// (catalog-camera-viewfinder-1472.spec.ts).
//
// getUserMedia is stubbed as available, so the only reason not to use it is
// the Android shell: a regression that drops the check fails on __gumCalls.

async function stubCameraAndCounters(page: Page, androidShell: boolean) {
  await page.addInitScript((androidShell) => {
    if (androidShell) (window as any).AndroidKiosk = {};
    (window as any).__gumCalls = 0;
    (window as any).__cameraInputClicks = 0;
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        // pending forever: a rejection would make the page fall back to the
        // capture input, which is not what the desktop case is measuring
        getUserMedia: () => {
          (window as any).__gumCalls++;
          return new Promise(() => {});
        },
      },
    });
    document.addEventListener('DOMContentLoaded', () => {
      // Count, never open: a real chooser would block the headless page.
      const camera = document.getElementById('image-file-camera') as HTMLInputElement | null;
      if (camera) camera.click = () => { (window as any).__cameraInputClicks++; };
    });
  }, androidShell);
}

async function openItemImagePanel(page: Page) {
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Butter 250g' }).click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator('#item-form-panel-image')).toBeVisible();
}

test.describe('Android app: Take a photo opens the system camera', () => {
  test('clicks the capture input, never the in-page viewfinder', async ({ page }) => {
    await stubCameraAndCounters(page, true);
    await openItemImagePanel(page);
    const take = page.locator('#image-camera-btn');
    await expect(take).toBeVisible();
    await take.click();
    expect(await page.evaluate(() => (window as any).__cameraInputClicks)).toBe(1);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(0);
    await expect(page.locator('#image-viewfinder')).toBeHidden();
  });
});

test.describe('Android app: the photo the system camera returns', () => {
  test('is shrunk to 1024px; the background offer comes first, then it saves with no Upload Image tap', async ({ page }) => {
    await stubCameraAndCounters(page, true);
    // remember the `image` part of every FormData sent over XHR (htmx), so the
    // test can inspect what was uploaded without reading the page's input
    await page.addInitScript(() => {
      const send = XMLHttpRequest.prototype.send;
      XMLHttpRequest.prototype.send = function (body?: any) {
        if (body instanceof FormData && body.get('image') instanceof File) {
          (window as any).__uploadedImage = body.get('image');
        }
        return send.call(this, body);
      };
    });
    await openItemImagePanel(page);
    const upload = page.waitForRequest((r) => r.url().includes('/api/catalog/item/image') && r.method() === 'POST');
    // what the shell hands back: a big camera JPEG in the capture input
    const sent = await page.evaluate(async () => {
      const c = document.createElement('canvas');
      c.width = 3264; c.height = 2448; // 8MP, over the server's 6MP limit
      c.getContext('2d')!.fillRect(0, 0, c.width, c.height);
      const blob: Blob = await new Promise((res) => c.toBlob((b) => res(b!), 'image/jpeg', 0.9));
      const dt = new DataTransfer();
      dt.items.add(new File([blob], 'IMG_0001.jpg', { type: 'image/jpeg' }));
      const input = document.getElementById('image-file-camera') as HTMLInputElement;
      input.files = dt.files;
      input.dispatchEvent(new Event('change', { bubbles: true }));
      return blob.size;
    });
    expect(sent).toBeGreaterThan(0);
    // the Remove background offer comes first (as after Capture); "Use
    // original photo" is the decision and saves at once
    await page.getByRole('button', { name: 'Use original photo' }).click();
    await upload; // saved with no tap on Upload Image
    // ut-docs#6151: assert on the file that was actually POSTed, not on the
    // #image-file input. The page clears that input when the upload's
    // RESPONSE arrives, and awaiting the request does not order a read of it
    // before that, so reading files[0] here flaked (10/50 under --repeat-each
    // 50 --workers 4). (Chromium gives
    // Playwright no postDataBuffer() for a file-backed multipart body, so the
    // `image` part is captured from the FormData at send time, see addInitScript above.)
    const dims = await page.evaluate(async () => {
      const f = (window as any).__uploadedImage as File | undefined;
      if (!f) return null;
      const bmp = await createImageBitmap(f);
      return { w: bmp.width, h: bmp.height, type: f.type };
    });
    expect(dims, 'an image part was sent in the upload').not.toBeNull();
    expect(dims!.type).toBe('image/jpeg');
    expect(dims!.w).toBeGreaterThan(0);
    expect(dims!.h).toBeGreaterThan(0);
    // shrinkPhoto scales the 3264x2448 original so the long edge is exactly 1024
    expect(Math.max(dims!.w, dims!.h)).toBe(1024);
  });
});

test.describe('Desktop browser: Take a photo still uses the viewfinder', () => {
  test('asks getUserMedia, not the capture input', async ({ page }) => {
    await stubCameraAndCounters(page, false);
    await openItemImagePanel(page);
    await page.locator('#image-camera-btn').click();
    await expect.poll(() => page.evaluate(() => (window as any).__gumCalls)).toBeGreaterThan(0);
    expect(await page.evaluate(() => (window as any).__cameraInputClicks)).toBe(0);
  });
});
