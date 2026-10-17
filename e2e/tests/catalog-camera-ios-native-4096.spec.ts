import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';

// ut-docs#4096: on iPhone and iPad the in-page viewfinder showed a black box
// that then went blank inside the app. Product owner decision (2026-10-15):
// on iOS/iPadOS the item photo uses the system's own picker instead — better
// photos, familiar, no in-page video. iOS always offers Photo Library / Take
// Photo / Files for an image input, so a separate "Take a photo" button would
// duplicate it: there is one "Add photo" button. Android and desktop keep
// both buttons and the viewfinder (#1472), covered by
// catalog-camera-viewfinder-1472.spec.ts.
//
// getUserMedia is stubbed as available, so the only reason not to use it is
// the platform: a regression that drops the iOS check fails on __gumCalls.

const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148';
// iPadOS asks for desktop pages by default: a Mac user agent, told apart
// from a real Mac only by its touch points.
const IPAD_DESKTOP_UA =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15';

const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');

async function stubCameraAndCounters(page: Page, maxTouchPoints: number) {
  await page.addInitScript((maxTouchPoints) => {
    (window as any).__gumCalls = 0;
    (window as any).__cameraInputClicks = 0;
    Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, get: () => maxTouchPoints });
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          (window as any).__gumCalls++;
          throw new Error('getUserMedia must not be used on iOS (ut-docs#4096)');
        },
      },
    });
    (window as any).__fileInputClicks = 0;
    document.addEventListener('DOMContentLoaded', () => {
      // Count, never open: a real chooser would block the headless page.
      const camera = document.getElementById('image-file-camera') as HTMLInputElement | null;
      if (camera) camera.click = () => { (window as any).__cameraInputClicks++; };
      const file = document.getElementById('image-file') as HTMLInputElement | null;
      if (file) file.click = () => { (window as any).__fileInputClicks++; };
    });
  }, maxTouchPoints);
}

async function openItemImagePanel(page: Page) {
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Butter 250g' }).click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator('#item-form-panel-image')).toBeVisible();
}

for (const c of [
  { name: 'iPhone', ua: IPHONE_UA, touch: 5 },
  { name: 'iPad (desktop-class user agent)', ua: IPAD_DESKTOP_UA, touch: 5 },
]) {
  test.describe(`${c.name}: one Add photo button opens the system picker`, () => {
    test.use({ userAgent: c.ua });

    test('no Take a photo button, no viewfinder; Add photo opens the image input', async ({ page }) => {
      await stubCameraAndCounters(page, c.touch);
      await openItemImagePanel(page);
      await expect(page.locator('#image-camera-btn')).toBeHidden();
      const add = page.locator('#image-choose-btn');
      await expect(add).toHaveText('Add photo');
      await add.click();
      await expect.poll(() => page.evaluate(() => (window as any).__fileInputClicks)).toBe(1);
      expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(0);
      await expect(page.locator('#image-viewfinder')).toBeHidden();
    });

    // "Retake" from the background-removal offer (photo-cutout.js) clicks
    // the hidden camera button: it must reach the system camera, never the
    // viewfinder.
    test('a programmatic Take a photo (Retake) opens the system camera', async ({ page }) => {
      await stubCameraAndCounters(page, c.touch);
      await openItemImagePanel(page);
      await page.evaluate(() => (document.getElementById('image-camera-btn') as HTMLButtonElement).click());
      await expect.poll(() => page.evaluate(() => (window as any).__cameraInputClicks)).toBe(1);
      expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(0);
    });

    // A native app saves the photo the moment it is chosen: no second
    // "Upload Image" tap to forget (and lose the photo by closing the item).
    // The background-removal offer (#3127/#4137) still comes first; "Use
    // original photo" is the decision.
    test('a picked photo saves without an Upload Image tap', async ({ page }) => {
      await stubCameraAndCounters(page, c.touch);
      await openItemImagePanel(page);
      await page.locator('#image-file').setInputFiles({ name: 'pick.png', mimeType: 'image/png', buffer: PNG });
      await page.locator('#image-cutout [data-cutout-action="original"]').click();
      await expect(page.locator('#image-msg')).toContainText('updated');
      await expect(page.locator('#image-file-name')).toHaveText('');
    });

    test('a photo from the camera (Retake path) saves the same way', async ({ page }) => {
      await stubCameraAndCounters(page, c.touch);
      await openItemImagePanel(page);
      await page.locator('#image-file-camera').setInputFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });
      await page.locator('#image-cutout [data-cutout-action="original"]').click();
      await expect(page.locator('#image-msg')).toContainText('updated');
    });
  });
}

// A real Mac reports the same user agent as an iPad but no touch points:
// it keeps the in-page viewfinder.
test.describe('Mac (no touch): keeps the in-page viewfinder', () => {
  test.use({ userAgent: IPAD_DESKTOP_UA });

  test('both buttons stay, and getUserMedia is used', async ({ page }) => {
    await stubCameraAndCounters(page, 0);
    await openItemImagePanel(page);
    await expect(page.locator('#image-camera-btn')).toBeVisible();
    await expect(page.locator('#image-choose-btn')).toHaveText('Choose File');
    await page.locator('#image-camera-btn').click();
    await expect.poll(() => page.evaluate(() => (window as any).__gumCalls)).toBe(1);
  });
});
