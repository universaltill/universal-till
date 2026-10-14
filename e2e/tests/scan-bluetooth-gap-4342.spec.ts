import { test, expect } from './fixtures';
import type { Page, Request } from '@playwright/test';

// ut-docs#4342: the owner's Bluetooth scanner ("NT barcode scanner") sends
// one key every 62.5 ms, and now and then skips a report slot — measured on
// the tablet with getevent: a single 124 ms gap inside an otherwise steady
// burst. app.js reset its scan buffer on any gap over 100 ms, so only the
// digits after the hiccup were submitted ("9" for voucher 5000000000029,
// which then matched an item by name). The clock is Playwright's fake one,
// so the gaps here are exactly the measured ones, not best-effort timing.

const VOUCHER = '5000000000029';

// Load the sale screen with a fake clock that only moves on runFor(). A
// merely installed clock keeps running in real time, so each key press's own
// latency (tens of ms on a CI runner) added onto every gap and pushed the
// 100 ms burst's median past the scanner threshold.
async function openWithPausedClock(page: Page) {
  await page.clock.install();
  await page.goto('/');
  // +1000: pauseAt refuses a time in the past, and the clock may tick
  // between the evaluate and the pause.
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 1000);
}

async function pressWithGaps(page: Page, keys: string[], gaps: number[]) {
  for (let i = 0; i < keys.length; i++) {
    if (i > 0) await page.clock.runFor(gaps[i - 1]);
    await page.keyboard.press(keys[i]);
  }
}

function scanForm(req: Request): URLSearchParams {
  return new URLSearchParams(req.postData() || '');
}

test.describe('scanner bursts with a Bluetooth hiccup (ut-docs#4342)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a 124 ms gap mid-scan still submits the whole code, marked as a scanner read', async ({ page }) => {
    await openWithPausedClock(page);
    const code = page.locator('form.scan-row input[name="code"]');
    await expect(code).toBeFocused();

    const keys = [...VOUCHER.split(''), 'Enter'];
    const gaps = keys.slice(1).map(() => 62);
    gaps[5] = 124; // the measured missed slot, before the 7th digit
    const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
    await pressWithGaps(page, keys, gaps);
    const form = scanForm(await req);
    expect(form.get('code')).toBe(VOUCHER);
    expect(form.get('src')).toBe('scanner');
  });

  // Review finding: a Bluetooth scanner on a slow transmit setting sends
  // ~100 ms per character — still a scanner, not a person typing.
  test('a scanner on a slow 100 ms transmit setting is still a scanner read', async ({ page }) => {
    await openWithPausedClock(page);
    const keys = [...VOUCHER.split(''), 'Enter'];
    const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
    await pressWithGaps(page, keys, keys.slice(1).map(() => 100));
    const form = scanForm(await req);
    expect(form.get('code')).toBe(VOUCHER);
    expect(form.get('src')).toBe('scanner');
  });

  // Review finding: pin both halves of the rule, not just "fast" vs "slow".
  test('two slow gaps in one burst is not a scanner read', async ({ page }) => {
    await openWithPausedClock(page);
    const code = page.locator('form.scan-row input[name="code"]');
    const keys = [...VOUCHER.split(''), 'Enter'];
    const gaps = keys.slice(1).map(() => 62);
    gaps[3] = 124;
    gaps[8] = 124;
    const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
    await code.focus();
    await pressWithGaps(page, keys, gaps);
    const form = scanForm(await req);
    expect(form.get('src') || '').toBe('');
  });

  test('a steady 130 ms burst is over the scanner threshold', async ({ page }) => {
    await openWithPausedClock(page);
    const code = page.locator('form.scan-row input[name="code"]');
    const keys = [...VOUCHER.split(''), 'Enter'];
    const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
    await code.focus();
    await pressWithGaps(page, keys, keys.slice(1).map(() => 130));
    const form = scanForm(await req);
    expect(form.get('src') || '').toBe('');
  });

  test('slow typing in the Barcode box is typed entry, not a scanner read', async ({ page }) => {
    await openWithPausedClock(page);
    const keys = [...'Cola'.split(''), 'Enter'];
    const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
    await pressWithGaps(page, keys, keys.slice(1).map(() => 180));
    const form = scanForm(await req);
    expect(form.get('code')).toBe('Cola');
    expect(form.get('src') || '').toBe('');
  });
});

test('the camera submits a code only after reading it twice in a row (ut-docs#4342)', async ({ page }) => {
  await page.addInitScript((full: string) => {
    // A partial first read, then the real code: the fragment must never
    // reach /api/pos/scan.
    (window as any).__seq = ['9', full, full];
    (window as any).BarcodeDetector = class {
      async detect() {
        const seq = (window as any).__seq as string[];
        const v = seq.length ? seq.shift() : full;
        return [{ rawValue: v }];
      }
    };
    const canvas = document.createElement('canvas');
    canvas.width = 10;
    canvas.height = 10;
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: async () => (canvas as any).captureStream() },
    });
  }, VOUCHER);
  await page.goto('/');
  const posted: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/api/pos/scan') && r.method() === 'POST') posted.push(r.postData() || '');
  });
  const req = page.waitForRequest((r) => r.url().includes('/api/pos/scan') && r.method() === 'POST');
  await page.locator('#barcode-scan-open').click();
  const form = scanForm(await req);
  expect(form.get('code')).toBe(VOUCHER);
  expect(form.get('src')).toBe('camera');
  expect(posted).toHaveLength(1);
  await page.request.post('/api/pos/reset');
});
