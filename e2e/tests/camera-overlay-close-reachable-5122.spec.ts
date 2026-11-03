import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5122: on a short landscape screen (740x360, 640x320) the camera
// overlay's 4:3 video filled the capped .ai-identify-panel, so Close sat
// below the panel's visible area and could only be reached by scrolling
// inside it (elementFromPoint at its centre was null). The controls row
// must stay on screen without any scroll, also when long content (a
// localized camera error) overflows the panel.

// Same camera stub as camera-overlay-clear-of-statusbar-3960.spec.ts.
async function stubCamera(page: Page) {
  await page.addInitScript(() => {
    (window as any).BarcodeDetector = class {
      async detect() { return []; }
    };
    const canvas = document.createElement('canvas');
    canvas.width = 10;
    canvas.height = 10;
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: async () => (canvas as any).captureStream() },
    });
  });
}

const VIEWPORTS = [
  { width: 740, height: 360, phone: false },
  { width: 640, height: 320, phone: false },
  { width: 1024, height: 600, phone: false },
  { width: 360, height: 740, phone: true },
];

async function closeHit(page: Page) {
  return page.evaluate(() => {
    const panel = document.querySelector('#barcode-scan-overlay .ai-identify-panel') as HTMLElement;
    const btn = document.querySelector('#barcode-scan-close')!;
    const r = btn.getBoundingClientRect();
    const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      hit: !!el && !!el.closest('#barcode-scan-close'),
      what: el ? `${el.tagName.toLowerCase()}#${el.id}.${el.className}` : 'null',
      top: r.top,
      bottom: r.bottom,
      vh: innerHeight,
      scrollTop: panel.scrollTop,
      overflows: panel.scrollHeight > panel.clientHeight + 1,
    };
  });
}

for (const vp of VIEWPORTS) {
  for (const lang of ['', 'fa']) {
    test.describe(`camera overlay Close reachable at ${vp.width}x${vp.height}${lang ? ' (' + lang + ', RTL)' : ''} (ut-docs#5122)`, () => {
      test.use({ viewport: { width: vp.width, height: vp.height }, hasTouch: vp.phone, isMobile: vp.phone });
      test.afterEach(async ({ page }) => {
        await page.request.post('/api/pos/reset');
      });

      test('Close is the hit target at its centre without scrolling', async ({ page }) => {
        const assertClean = watchConsole(page);
        await stubCamera(page);
        await page.goto(lang ? `/?lang=${lang}` : '/');
        if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
        // Phone: the scan row lives in the tender sheet, closed until the basket bar is tapped.
        if (vp.phone) await page.locator('.basket-phonebar').click();
        const openBtn = page.locator('#barcode-scan-open');
        await expect(openBtn).toBeVisible();
        await openBtn.click();
        await expect(page.locator('#barcode-scan-overlay .ai-identify-panel')).toBeVisible();
        // The stubbed stream has a real 4:3 box from CSS, not from metadata.
        await expect(page.locator('#barcode-scan-video')).toBeVisible();

        const check = (m: Awaited<ReturnType<typeof closeHit>>, what: string) => {
          const msg = `${what}: Close [${m.top.toFixed(1)}, ${m.bottom.toFixed(1)}] viewport h=${m.vh}, centre hits ${m.what}, panel scrollTop=${m.scrollTop}`;
          expect(m.scrollTop, `panel must not have been scrolled: ${msg}`).toBe(0);
          expect(m.bottom, `Close inside the viewport: ${msg}`).toBeLessThanOrEqual(m.vh);
          expect(m.hit, `Close must be the hit target at its centre: ${msg}`).toBe(true);
        };
        check(await closeHit(page), 'natural height');

        // Overflowing content drives the panel to its cap and makes it scroll;
        // Close must still sit on screen at the panel's bottom edge.
        await page.locator('#barcode-scan-status').evaluate((el) => { el.textContent = 'Camera unavailable. '.repeat(120); });
        const capped = await closeHit(page);
        expect(capped.overflows, 'the long status must overflow the panel, or this leg tests nothing').toBe(true);
        check(capped, 'overflowing content');

        // A real tap on the button's centre closes the overlay.
        const box = (await page.locator('#barcode-scan-close').boundingBox())!;
        await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
        await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
        assertClean();
      });
    });
  }
}
