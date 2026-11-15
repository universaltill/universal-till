import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#3960: the camera overlays (.ai-identify-overlay > .ai-identify-panel,
// used by #barcode-scan-overlay) centre a panel capped at 90dvh. At 1024x600
// that puts its bottom edge ~3px under the 33px sticky `.statusbar` (z 90,
// above the overlay's z 60), hiding the panel's last row. The panel must end
// at or above the status bar's top edge (and start inside the viewport).

// Same camera stub as sale-screen-camera-barcode-scan-548.spec.ts: a
// canvas-backed getUserMedia, so no real camera or permission prompt.
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
  { width: 1024, height: 600, phone: false },
  { width: 360, height: 740, phone: true },
];

async function measure(page: Page) {
  return page.evaluate(() => {
    const panel = document.querySelector('#barcode-scan-overlay .ai-identify-panel')!.getBoundingClientRect();
    const bar = document.querySelector('.statusbar');
    const barVisible = !!bar && getComputedStyle(bar).display !== 'none';
    const barRect = bar ? bar.getBoundingClientRect() : null;
    return {
      panelTop: panel.top,
      panelBottom: panel.bottom,
      vh: innerHeight,
      barVisible,
      barTop: barRect ? barRect.top : null,
      barBottom: barRect ? barRect.bottom : null,
    };
  });
}

for (const vp of VIEWPORTS) {
  for (const lang of ['', 'fa']) {
    test.describe(`camera overlay clear of the status bar at ${vp.width}x${vp.height}${lang ? ' (' + lang + ', RTL)' : ''} (ut-docs#3960)`, () => {
      test.use({ viewport: { width: vp.width, height: vp.height }, hasTouch: vp.phone, isMobile: vp.phone });
      test.afterEach(async ({ page }) => {
        await page.request.post('/api/pos/reset');
      });

      test('barcode-scan panel ends above .statusbar', async ({ page }) => {
        const assertClean = watchConsole(page);
        await stubCamera(page);
        await page.goto(lang ? `/?lang=${lang}` : '/');
        if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
        // Phone: the scan row lives in the tender sheet, closed until the basket bar is tapped.
        if (vp.phone) await page.locator('.basket-phonebar').click();
        if (vp.phone) {
          // The phone bar is display:none unless a problem chip shows (CI is
          // healthy), so inject one: the leg must really test the bar.
          await page.evaluate(() => {
            const c = document.createElement('span');
            c.className = 'sb-item sb-disk-low';
            c.textContent = 'test';
            document.querySelector('.statusbar')!.appendChild(c);
          });
          await expect(page.locator('.statusbar')).toBeVisible();
          await expect.poll(() => page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--phone-sb-h')) || 0)).toBeGreaterThan(0);
        }
        const openBtn = page.locator('#barcode-scan-open');
        await expect(openBtn).toBeVisible();
        await openBtn.click();
        await expect(page.locator('#barcode-scan-overlay')).toBeVisible();
        await expect(page.locator('#barcode-scan-overlay .ai-identify-panel')).toBeVisible();

        const check = (m: Awaited<ReturnType<typeof measure>>, what: string) => {
          const msg = `${what}: panel [${m.panelTop.toFixed(1)}, ${m.panelBottom.toFixed(1)}] statusbar top=${m.barTop} bottom=${m.barBottom} viewport h=${m.vh}`;
          expect(m.barVisible, `status bar must be visible (${msg})`).toBe(true);
          expect(m.panelTop, `panel top inside viewport: ${msg}`).toBeGreaterThanOrEqual(0);
          if (m.barVisible && m.barTop !== null && m.barBottom! > 0 && m.barTop < m.vh) {
            expect(m.panelBottom, `panel bottom must not run under .statusbar: ${msg}`).toBeLessThanOrEqual(m.barTop + 0.5);
            // Not merely touching: the natural 1024x600 panel ends ~2px above the
            // bar, and a bar wrapping to a second row would then cover it.
            expect(m.barTop - m.panelBottom, `panel needs breathing room above .statusbar: ${msg}`).toBeGreaterThanOrEqual(8);
          }
        };
        check(await measure(page), 'natural height');
        // Status stays reachable (offline-first rule): the bar's centre must hit
        // the bar itself, not the overlay above it.
        const hit = await page.evaluate(() => {
          const r = document.querySelector('.statusbar')!.getBoundingClientRect();
          const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return el ? `${el.tagName.toLowerCase()}#${el.id}` + (el.closest('.statusbar') ? ' (in statusbar)' : '') : 'null';
        });
        expect(hit, 'status bar must stay tappable while the camera overlay is open').toContain('(in statusbar)');
        if (vp.width === 1024) {
          // The kiosk floor shows the whole panel: it must not need to scroll.
          const sh = await page.locator('#barcode-scan-overlay .ai-identify-panel').evaluate((el) => ({ s: el.scrollHeight, c: el.clientHeight }));
          expect(sh.s, `panel scrolls at its natural height (scrollHeight ${sh.s} > clientHeight ${sh.c})`).toBeLessThanOrEqual(sh.c + 1);
        }

        // Overflowing content (a long localized camera error) drives the panel
        // to its max-block-size cap, which is where it used to reach the bar.
        await page.locator('#barcode-scan-status').evaluate((el) => { el.textContent = 'Camera unavailable. '.repeat(120); });
        check(await measure(page), 'capped height');

        await page.locator('#barcode-scan-close').click();
        await expect(page.locator('#barcode-scan-overlay')).toBeHidden();
        assertClean();
      });
    });
  }
}
