import { test, expect } from './fixtures';
import * as fs from 'fs';

// ut-docs#6175: the browser-print fallback of the receipt must print only the
// receipt -- no status bar, no "auto-reset" countdown line, no screen-panel
// frame around the basket. Screen rendering is unchanged (asserted first, so
// a print rule that leaks onto screen fails). window.print is stubbed; no
// printer or dialog is driven. Renders for a human look go to RENDER_DIR.

test.use({ viewport: { width: 1280, height: 800 } });

const RENDER_DIR = process.env.UT_6175_RENDER_DIR || '';

test.afterEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });

test('printed receipt hides status bar, auto-reset line and basket frame (ut-docs#6175)', async ({ page }) => {
  await page.addInitScript(() => { window.print = () => {}; });
  await page.request.post('/api/pos/reset');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.getByTestId('payment-open').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.locator('#payment-overlay').getByTestId('pay-default').click(),
  ]);
  const basket = page.locator('#basket.receipt-view');
  await expect(basket).toBeVisible();

  // Screen: all three present, so the print rules are print-only.
  await expect(page.locator('.statusbar')).toBeVisible();
  await expect(page.locator('.receipt-auto-reset')).toBeVisible();
  const screenBorder = await basket.evaluate((el) => parseFloat(getComputedStyle(el).borderTopWidth));
  expect(screenBorder).toBeGreaterThan(0);

  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('.statusbar')).toBeHidden();
  await expect(page.locator('.receipt-auto-reset')).toBeHidden();
  const frame = await basket.evaluate((el) => {
    const cs = getComputedStyle(el);
    return { w: cs.borderTopWidth, r: cs.borderTopLeftRadius, s: cs.boxShadow };
  });
  expect(frame).toEqual({ w: '0px', r: '0px', s: 'none' });

  // Receipt itself and the #6155 inset are untouched.
  await expect(page.locator('.receipt-card:not(.receipt-print-copy)')).toBeVisible();
  const pad = await page.locator('.receipt-wrap').evaluate((el) => parseFloat(getComputedStyle(el).paddingInlineStart));
  expect(pad).toBeGreaterThanOrEqual(6 * 96 / 25.4 - 0.5);

  if (RENDER_DIR) {
    fs.mkdirSync(RENDER_DIR, { recursive: true });
    await page.pdf({ path: `${RENDER_DIR}/receipt-6175-a4.pdf`, format: 'A4', preferCSSPageSize: true });
    await page.pdf({ path: `${RENDER_DIR}/receipt-6175-80mm.pdf`, width: '80mm', height: '250mm', preferCSSPageSize: false });
  }
});
