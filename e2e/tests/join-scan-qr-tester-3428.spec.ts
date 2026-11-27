import { test, expect } from './fixtures';

// ut-docs#3428 Tester additions: setup wizard path, no app.js on /setup,
// Cancel reachable at the 1024x600 kiosk floor, long-locale/RTL overlays.
const TOKEN = '0123456789abcdef0123456789abcdef';
const GOOD = Buffer.from(JSON.stringify({ url: 'http://192.168.1.10:8080', token: TOKEN })).toString('base64url');

async function stub(page: import('@playwright/test').Page, qr: string[]) {
  await page.addInitScript((qr) => {
    const w = window as any;
    w.BarcodeDetector = class {
      static async getSupportedFormats() { return ['qr_code']; }
      async detect() { return qr.length ? [{ rawValue: qr[0] }] : []; }
    };
    Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: {
      getUserMedia: async () => document.createElement('canvas').captureStream() } });
  }, qr);
}

async function openSetup(page: import('@playwright/test').Page, lang = 'en') {
  await page.goto('/setup?lang=' + lang);
  await page.locator('#setup-tab-code').waitFor({ state: 'attached' });
  if (!(await page.locator('#setup-tab-code').isVisible())) {
    // locale-independent: the wizard's secondary "Join an existing shop" button
    // (its label is translated, so a text match fails under ?lang=de)
    await page.locator('.setup-secondary-row button').first().click();
  }
  await page.locator('#setup-tab-code').click();
  await expect(page.locator('#setup-join-scan')).toBeVisible();
}

test.describe('join scan: Tester checks (ut-docs#3428)', () => {
  test('setup: no app.js on the page, scan still fills the form, no POST', async ({ page }) => {
    const posts: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/api/setup/join')) posts.push(r.url()); });
    await stub(page, [GOOD]);
    await openSetup(page);
    expect(await page.locator('script[src*="/public/app.js"]').count()).toBe(0);
    expect(await page.evaluate(() => typeof (window as any).utQrScan)).toBe('object');
    await page.locator('#setup-join-scan').click();
    await expect(page.locator('#setup-join-scan-overlay')).toBeHidden();
    const form = page.locator('form[hx-post="/api/setup/join"]');
    await expect(form.locator('input[name=code]')).toHaveValue(GOOD);
    await expect(page.locator('#setup-join-scanned bdi')).toHaveText('192.168.1.10:8080');
    await expect(page.locator('#setup-pairing-till-name')).toBeFocused();
    expect(posts).toEqual([]);
  });

  for (const [name, path] of [['setup', '/setup'], ['tills', '/tills?tab=add&need=join']] as const) {
    for (const lang of ['en', 'de']) {
      test(`1024x600 ${name} ${lang}: Cancel and Type-instead inside the viewport`, async ({ page }) => {
        await stub(page, []);
        await page.setViewportSize({ width: 1024, height: 600 });
        if (name === 'setup') await openSetup(page, lang);
        else { await page.goto('/tills?tab=add&need=join&lang=' + lang); await page.locator('#tills-tab-code').click(); }
        const p = name === 'setup' ? 'setup' : 'tills';
        await page.locator(`#${p}-join-scan`).click();
        await expect(page.locator(`#${p}-join-scan-overlay`)).toBeVisible();
        await expect(page.locator(`#${p}-join-scan-close`)).toBeFocused();
        for (const id of ['close', 'type']) {
          const b = await page.locator(`#${p}-join-scan-${id}`).boundingBox();
          expect(b, id).not.toBeNull();
          expect(b!.y).toBeGreaterThanOrEqual(0);
          expect(b!.y + b!.height).toBeLessThanOrEqual(600);
          expect(b!.height).toBeGreaterThanOrEqual(36);
        }
        await page.screenshot({ path: `test-results/tester-3428-${name}-${lang}-1024x600.png` });
        await page.locator(`#${p}-join-scan-close`).click();
        await expect(page.locator(`#${p}-join-scan`)).toBeFocused();
      });
    }
  }

  for (const lang of ['de', 'tr', 'fa']) {
    test(`360px overlay open + not-UT hint + chip (${lang})`, async ({ page }) => {
      await stub(page, ['https://example.com/x']);
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto('/tills?tab=add&need=join&lang=' + lang);
      await page.locator('#tills-tab-code').click();
      await page.locator('#tills-join-scan').click();
      await expect(page.locator('#tills-join-scan-status')).not.toHaveText('');
      await page.waitForTimeout(1500);
      await page.screenshot({ path: `test-results/tester-3428-overlay-${lang}-360.png` });
      for (const id of ['close', 'type']) {
        const b = await page.locator(`#tills-join-scan-${id}`).boundingBox();
        expect(b!.y + b!.height).toBeLessThanOrEqual(740);
        expect(b!.x).toBeGreaterThanOrEqual(0);
        expect(b!.x + b!.width).toBeLessThanOrEqual(360);
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
      expect(await page.evaluate(() => document.documentElement.dir)).toBe(lang === 'fa' ? 'rtl' : 'ltr');
    });
  }
});
