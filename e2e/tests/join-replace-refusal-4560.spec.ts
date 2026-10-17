import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4560: a join refused because this till holds sales must bring the
// replace-data tick box with it (the first sale can land after /tills
// rendered, so the page had none), and an unticked box that holds a join
// back must say so persistently — the native validation bubble alone may
// never show on a WebKitGTK kiosk.

async function ringUpOneSale(page) {
  await page.request.post('/api/pos/reset');
  const scan = await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  expect(scan.ok(), `scan: ${scan.status()}`).toBeTruthy();
  const tender = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: { payments: [{ method: 'cash', amount: 100000 }] },
  });
  expect(tender.ok(), `tender: ${tender.status()} ${await tender.text()}`).toBeTruthy();
}

test.describe('join refused for sales brings the tick box (ut-docs#4560)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of [
    { width: 1024, height: 600 },
    { width: 360, height: 740 },
  ]) {
    test(`code join at ${vp.width}x${vp.height}: refusal fills the slot; unticked retry is marked`, async ({ page }) => {
      await page.setViewportSize(vp);
      // The refusal is a deliberate 409; the browser logs every 4xx.
      const assertClean = watchConsole(page, /status of 409/);
      await page.goto('/tills');
      await page.waitForSelector('.tills-join');
      // Whatever other specs left on this shared till, put the page in the
      // state the card is about: rendered with no tick box, then a sale.
      await page.evaluate(() => { document.getElementById('pairing-replace-slot')!.innerHTML = ''; });
      await ringUpOneSale(page);

      await page.locator('#tills-tab-code').click();
      await page.locator('#pairing-till-name').fill('Till 4560');
      await page.locator('#tills-panel-code input[name="code"]').fill('ABC-DEF');
      await page.locator('#tills-panel-code input[name="address"]').fill('192.0.2.1:8080');
      const [resp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/sync/join')),
        page.locator('#tills-panel-code button[type=submit]').click(),
      ]);
      expect(resp.status()).toBe(409);

      // The refusal shows where the join's message goes, and the box lands
      // above the tabs.
      await expect(page.locator('#join-msg')).toContainText('✗');
      const box = page.locator('#pairing-replace-slot #pairing-replace-local');
      await expect(box).toHaveCount(1);
      await expect(page.locator('#pairing-replace-warning')).toBeVisible();
      await expect(box).not.toBeChecked();

      // Retry unticked: held back client-side, and marked persistently.
      let sent = 0;
      page.on('request', (r) => { if (r.url().includes('/api/sync/join')) sent++; });
      await page.locator('#tills-panel-code button[type=submit]').click();
      await expect(box).toHaveClass(/field-invalid/);
      await expect(page.locator('#pairing-replace-warning')).toHaveClass(/field-invalid/);
      expect(sent, 'an unticked retry must not reach the server').toBe(0);
      const outline = await page.locator('#pairing-replace-warning').evaluate((el) => getComputedStyle(el).outlineStyle);
      expect(outline).toBe('solid');
      await page.locator('#pairing-replace-slot').screenshot({ path: test.info().outputPath(`join-invalid-${vp.width}.png`) });

      // Ticking clears the mark, and the retry now carries the confirm.
      await page.locator('label[for="pairing-replace-local"]').click();
      await expect(box).toBeChecked();
      await expect(box).not.toHaveClass(/field-invalid/);
      await expect(page.locator('#pairing-replace-warning')).not.toHaveClass(/field-invalid/);
      // Answered here: past the confirm the real handler would dial the
      // made-up main till address and wait out its timeout.
      let retryBody = '';
      await page.route('**/api/sync/join', async (route) => {
        retryBody = route.request().postData() || '';
        await route.fulfill({ status: 200, contentType: 'text/html', body: '' });
      });
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/sync/join')),
        page.locator('#tills-panel-code button[type=submit]').click(),
      ]);
      expect(retryBody).toContain('replace_local_data=1');

      // No horizontal scroll with the slot filled.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow).toBeLessThanOrEqual(0);
      await page.screenshot({ path: test.info().outputPath(`join-refusal-${vp.width}.png`), fullPage: true });
      assertClean();
    });
  }
});
