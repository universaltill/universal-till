import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';
import en from '../../web/locales/en.json';

// ut-docs#4102: the sale screen's plugin camera-identify overlay
// (catalog.identify seam, ADR-0121 §7) and its camera error branches. Core's
// built-in AI identify took its e2e spec with it (#2851); the plugin overlay
// has the same branches but no plugin answers catalog.identify on the shared
// worker till. This file drives it on its OWN till, with a real compiled WASM
// guest (internal/plugins/testdata/identify_guest, installed by
// e2e/seed_identify_plugin) that answers every photo with one suggestion for
// Coca-Cola 330ml. getUserMedia is stubbed from an offscreen canvas, so no
// real camera or permission prompt is involved. Expected strings come from
// web/locales/en.json (the overlay's data-msg-* attributes render those).
test.describe.configure({ mode: 'serial' });

// The seeded label, not the guest's built-in default: proves the guest read its settings.
const SKU_LABEL = 'Sample identified drink';
const ITEM_NAME = 'Coca-Cola 330ml'; // SKU 2000010000012 (internal/data/seeddata/demo_catalogue.sql)
const L = en as Record<string, string>;

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_identify_plugin'] });
  context = await browser.newContext({ baseURL: till.url, viewport: { width: 1024, height: 600 } });
  // getUserMedia stub, steered per test through window.__gumError (a
  // DOMException name to reject with). The canvas is repainted every frame so
  // captureStream() keeps delivering frames and video.videoWidth becomes > 0.
  // Every track stop() is counted on window.__stopCalls.
  await context.addInitScript(() => {
    const w = window as any;
    w.__gumError = '';
    w.__gumCalls = 0;
    w.__stopCalls = 0;
    const canvas = document.createElement('canvas');
    canvas.width = 320;
    canvas.height = 240;
    const ctx = canvas.getContext('2d')!;
    let n = 0;
    const paint = () => {
      ctx.fillStyle = n++ % 2 ? '#c33' : '#36c';
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      requestAnimationFrame(paint);
    };
    paint();
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          w.__gumCalls++;
          if (w.__gumError) throw new DOMException('stubbed', w.__gumError);
          const stream = (canvas as any).captureStream(15);
          stream.getTracks().forEach((t: MediaStreamTrack) => {
            const orig = t.stop.bind(t);
            t.stop = () => {
              w.__stopCalls++;
              orig();
            };
          });
          return stream;
        },
      },
    });
  });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

test.beforeEach(async () => {
  await page.goto('/?lang=en');
  await expect(page.locator('#plugin-identify-open')).toBeVisible();
});

const overlay = () => page.locator('#plugin-identify-overlay');
const status = () => page.locator('#plugin-identify-status');

async function openWith(gumError: string) {
  await page.evaluate((e) => ((window as any).__gumError = e), gumError);
  await page.locator('#plugin-identify-open').click();
  await expect(overlay()).toBeVisible();
}

test('the overlay carries the catalogue strings the branches below assert (ut-docs#4102)', async () => {
  // Guards the data-* wiring itself: the expected text is read from en.json,
  // so a template that rendered another key would fail here and below.
  const a = await overlay().evaluate((el) => ({ ...(el as HTMLElement).dataset }));
  expect(a.msgCameraError).toBe(L['ai.identify.camera_error']);
  expect(a.msgCameraBusy).toBe(L['ai.identify.camera_busy']);
  expect(a.msgCameraNotFound).toBe(L['ai.identify.camera_not_found']);
  expect(a.msgCameraPermissionDenied).toBe(L['ai.identify.camera_permission_denied']);
});

test('no navigator.mediaDevices (insecure context) shows the generic camera error (ut-docs#4102)', async () => {
  const done = watchConsole(page);
  await page.evaluate(() => Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: undefined }));
  await page.locator('#plugin-identify-open').click();
  await expect(overlay()).toBeVisible();
  await expect(status()).toHaveText(L['ai.identify.camera_error']);
  done();
});

for (const c of [
  { err: 'NotAllowedError', key: 'ai.identify.camera_permission_denied' },
  { err: 'NotFoundError', key: 'ai.identify.camera_not_found' },
  { err: 'NotReadableError', key: 'ai.identify.camera_busy' },
]) {
  test(`getUserMedia rejecting with ${c.err} shows ${c.key} (ut-docs#4102)`, async () => {
    const done = watchConsole(page);
    await openWith(c.err);
    await expect(status()).toHaveText(L[c.key]);
    expect(await page.evaluate(() => (window as any).__gumCalls)).toBe(1);
    // Close still works after a camera failure and clears the message.
    await page.locator('#plugin-identify-close').click();
    await expect(overlay()).toBeHidden();
    await expect(status()).toHaveText('');
    done();
  });
}

test('capture -> plugin suggestion -> tap adds the line, closes the overlay and stops the camera (ut-docs#4102)', async () => {
  const done = watchConsole(page);
  await openWith('');
  await expect(status()).toHaveText('');

  // Capture needs a live frame (videoWidth > 0) or it reports a camera error.
  await expect.poll(() => page.locator('#plugin-identify-video').evaluate((v: HTMLVideoElement) => v.videoWidth)).toBeGreaterThan(0);

  // The overlay is a plain fixed div, never a <dialog> (no showModal on till
  // surfaces): a cheap structural check, not proof the screen stays usable.
  expect(await page.evaluate(() => document.querySelectorAll('dialog[open]').length)).toBe(0);
  await expect(page.locator('#plugin-identify-overlay')).not.toHaveJSProperty('tagName', 'DIALOG');

  await page.locator('#plugin-identify-capture').click();
  const pick = page.locator('#plugin-identify-results [data-identify-pick]');
  await expect(pick).toHaveCount(1, { timeout: 15_000 });
  await expect(pick).toContainText(SKU_LABEL);
  await expect(page.locator('#plugin-identify-retake')).toBeVisible();

  // At the 1024x600 floor the suggestion and Close must be on screen.
  const vh = page.viewportSize()!.height;
  for (const loc of [pick, page.locator('#plugin-identify-close')]) {
    const box = await loc.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.y + box!.height).toBeLessThanOrEqual(vh);
  }

  await pick.click();
  await expect(page.locator('#basket-lines .line-name')).toHaveText([ITEM_NAME]);
  await expect(overlay()).toBeHidden();
  await expect.poll(() => page.evaluate(() => (window as any).__stopCalls)).toBeGreaterThan(0);
  done();
});
