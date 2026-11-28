import { test, expect } from './fixtures';
import { watchConsole, gotoTillsSection } from './helpers';

// ut-docs#3428: "Scan QR code" on the join form. Fill-then-confirm (UX
// verdict, design section 10): a valid scan fills the code and shows the
// host, but NEVER posts; the person adds a till name and presses Join.
// A real camera cannot run in CI, so getUserMedia returns a canvas stream
// and BarcodeDetector is stubbed (same pattern as #1292/#4342). The real
// decoder against a real QR picture is covered by the WebKit spec.

const TOKEN = '0123456789abcdef0123456789abcdef'; // newLongEnrolToken: 32 hex chars
function code(url: string, token = TOKEN): string {
  return Buffer.from(JSON.stringify({ url, token })).toString('base64url');
}
const GOOD = code('http://192.168.1.10:8080');

async function stubCamera(page: import('@playwright/test').Page, opts: { reject?: string; qr?: string[]; gate?: boolean }) {
  await page.addInitScript(({ reject, qr, gate }) => {
    const w = window as any;
    w.__stops = 0;
    w.__reads = qr || [];
    // gate: hold the stubbed QR back until the test arms it, so the overlay
    // stays open long enough for the test to observe it (no race with the
    // two-identical-reads auto-close).
    w.__armed = !gate;
    w.BarcodeDetector = class {
      static async getSupportedFormats() { return ['qr_code']; }
      async detect() {
        const v = w.__armed && w.__reads.length ? w.__reads[0] : null;
        return v ? [{ rawValue: v }] : [];
      }
    };
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: {
        getUserMedia: async () => {
          if (reject) throw new DOMException('x', reject);
          const s = document.createElement('canvas').captureStream();
          s.getTracks().forEach((t) => {
            const stop = t.stop.bind(t);
            t.stop = () => { w.__stops++; stop(); };
          });
          return s;
        },
      },
    });
  }, { reject: opts.reject ?? '', qr: opts.qr ?? [], gate: !!opts.gate });
}

async function openJoin(page: import('@playwright/test').Page) {
  await gotoTillsSection(page, 'add', '?need=join');
  await page.locator('#tills-tab-code').click();
  await expect(page.locator('#tills-join-scan')).toBeVisible();
}

test.describe('scan the pairing QR on the join form (ut-docs#3428)', () => {
  test('parse table', async ({ page }) => {
    await gotoTillsSection(page, 'add', '?need=join');
    const cases: [string, string, boolean][] = [
      ['form A base64url', GOOD, true],
      ['form A https', code('https://till.example:8443'), true],
      ['raw JSON', JSON.stringify({ url: 'http://192.168.1.10:8080', token: TOKEN }), true],
      ['padded with spaces', '  ' + GOOD + '\n', true],
      ['website', 'https://universaltill.com/', false],
      ['url with path+query', code('http://a/x?y=1'), false],
      ['url with userinfo', code('http://user:pw@192.168.1.10:8080'), false],
      ['javascript url', code('javascript:alert(1)'), false],
      ['missing token', Buffer.from(JSON.stringify({ url: 'http://192.168.1.10:8080' })).toString('base64url'), false],
      ['short token', code('http://192.168.1.10:8080', 'abc'), false],
      ['short code', 'K7P-4XQ', false],
      ['cloud 8-char code', 'ABCD-EFGH', false],
      ['2 KB payload', 'A'.repeat(2048), false],
      ['empty', '', false],
      ['ean13', '5012345678900', false],
    ];
    for (const [name, raw, ok] of cases) {
      const r = await page.evaluate((s) => (window as any).utQrScan.parse(s), raw);
      expect(r.ok, name).toBe(ok);
      if (ok) {
        // The code handed to the form is the scanned string, trimmed, never re-encoded.
        expect(r.code, name).toBe(raw.trim());
        expect(typeof r.host).toBe('string');
      }
    }
    const host = await page.evaluate((s) => (window as any).utQrScan.parse(s).host, GOOD);
    expect(host).toBe('192.168.1.10:8080');
  });

  test('valid scan fills the form, shows the host and posts nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const posts: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/api/sync/join')) posts.push(r.url()); });
    await stubCamera(page, { qr: [GOOD], gate: true });
    await openJoin(page);
    await page.locator('#pairing-till-name').fill('');
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scan-overlay')).toBeVisible();
    await page.evaluate(() => { (window as any).__armed = true; });
    await expect(page.locator('#tills-join-scan-overlay')).toBeHidden(); // closes itself after two identical reads
    const form = page.locator('form[hx-post="/api/sync/join"]');
    await expect(form.locator('input[name=code]')).toHaveValue(GOOD);
    await expect(form.locator('input[name=code]')).toHaveAttribute('readonly', '');
    await expect(form.locator('input[name=address]')).toHaveValue('');
    const chip = page.locator('#tills-join-scanned');
    await expect(chip).toBeVisible();
    await expect(chip).toContainText('Code scanned from');
    await expect(chip.locator('bdi')).toHaveText('192.168.1.10:8080');
    // The token is never shown in the DOM text or any attribute outside the code field.
    expect(await chip.innerText()).not.toContain(TOKEN);
    expect(await chip.innerHTML()).not.toContain(TOKEN);
    await expect(page.locator('#pairing-till-name')).toBeFocused();
    await expect(page.locator('#tills-join-scan-next')).toHaveText('Code scanned. Enter a till name and press Join.');
    expect(await page.evaluate(() => (window as any).__stops)).toBeGreaterThan(0);
    expect(posts, 'a scan alone must not send the join').toEqual([]);
    assertClean();
  });

  test('Join after a scan sends the scanned code; empty name shows the existing error', async ({ page }) => {
    const bodies: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/api/sync/join')) bodies.push(r.postData() || ''); });
    await stubCamera(page, { qr: [GOOD] });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scanned')).toBeVisible();
    const form = page.locator('form[hx-post="/api/sync/join"]');
    await form.locator('button[type=submit]').click();
    await expect(page.locator('#pairing-till-name-error')).toBeVisible();
    await expect(form.locator('input[name=code]')).toHaveValue(GOOD);
    expect(bodies).toEqual([]);
    await page.locator('#pairing-till-name').fill('Till 2');
    await form.locator('button[type=submit]').click();
    await expect.poll(() => bodies.length).toBe(1);
    expect(new URLSearchParams(bodies[0]).get('code')).toBe(GOOD);
  });

  test('Clear resets the field and returns focus to Scan', async ({ page }) => {
    await stubCamera(page, { qr: [GOOD] });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scanned')).toBeVisible();
    await page.locator('#tills-join-scan-clear').click();
    const codeInput = page.locator('form[hx-post="/api/sync/join"] input[name=code]');
    await expect(codeInput).toHaveValue('');
    await expect(codeInput).not.toHaveAttribute('readonly', '');
    await expect(page.locator('#tills-join-scanned')).toBeHidden();
    await expect(page.locator('#tills-join-scan')).toBeFocused();
  });

  test('permission denied explains, and Type-instead focuses the code field', async ({ page }) => {
    await stubCamera(page, { reject: 'NotAllowedError' });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scan-status')).toContainText('Camera access was denied');
    await page.locator('#tills-join-scan-type').click();
    await expect(page.locator('#tills-join-scan-overlay')).toBeHidden();
    await expect(page.locator('form[hx-post="/api/sync/join"] input[name=code]')).toBeFocused();
  });

  test('no camera and busy camera each get their own message', async ({ page }) => {
    await stubCamera(page, { reject: 'NotFoundError' });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scan-status')).toContainText('No camera is available here');
    await expect(page.locator('form[hx-post="/api/sync/join"] input[name=address]')).toBeVisible();
  });

  test('camera busy', async ({ page }) => {
    await stubCamera(page, { reject: 'NotReadableError' });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scan-status')).toContainText('Camera is in use by another app');
  });

  test('a non-Universal-Till QR keeps the overlay open with the hint', async ({ page }) => {
    await stubCamera(page, { qr: ['https://universaltill.com/'] });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scan-status')).toContainText('not a Universal Till pairing code');
    await expect(page.locator('#tills-join-scan-overlay')).toBeVisible();
    await expect(page.locator('form[hx-post="/api/sync/join"] input[name=code]')).toHaveValue('');
  });

  test('Cancel, Escape and pagehide stop every camera track and return focus', async ({ page }) => {
    await stubCamera(page, { qr: [] });
    await openJoin(page);
    for (const how of ['button', 'escape', 'pagehide'] as const) {
      const before = await page.evaluate(() => (window as any).__stops);
      await page.locator('#tills-join-scan').click();
      await expect(page.locator('#tills-join-scan-overlay')).toBeVisible();
      await expect(page.locator('#tills-join-scan-close')).toBeFocused();
      if (how === 'button') await page.locator('#tills-join-scan-close').click();
      else if (how === 'escape') await page.keyboard.press('Escape');
      else await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
      await expect(page.locator('#tills-join-scan-overlay')).toBeHidden();
      await expect.poll(() => page.evaluate(() => (window as any).__stops)).toBeGreaterThan(before);
      if (how !== 'pagehide') await expect(page.locator('#tills-join-scan')).toBeFocused();
    }
  });

  test('no console output mentions the token', async ({ page }) => {
    const logs: string[] = [];
    page.on('console', (m) => logs.push(m.text()));
    await stubCamera(page, { qr: [GOOD] });
    await openJoin(page);
    await page.locator('#tills-join-scan').click();
    await expect(page.locator('#tills-join-scanned')).toBeVisible();
    for (const l of logs) {
      expect(l).not.toContain(TOKEN);
      expect(l).not.toContain(GOOD);
    }
  });

  for (const lang of ['de', 'fa']) {
    test(`overlay and chip do not overflow a 360px phone (${lang})`, async ({ page }) => {
      await stubCamera(page, { qr: [GOOD] });
      await page.setViewportSize({ width: 360, height: 740 });
      await gotoTillsSection(page, 'add', '?need=join&lang=' + lang);
      await page.locator('#tills-tab-code').click();
      await page.locator('#tills-join-scan').click();
      await page.screenshot({ path: `test-results/join-scan-overlay-${lang}.png` });
      await expect(page.locator('#tills-join-scanned')).toBeVisible();
      await page.screenshot({ path: `test-results/join-scan-chip-${lang}.png` });
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(over).toBeLessThanOrEqual(0);
    });
  }
});
