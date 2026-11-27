import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { openPhoneSheet } from './helpers';

// ut-docs#7107: the tip row of the payment panel. The row only exists while
// payments.tips_enabled is on, so this spec switches it on for itself and off
// again after each test (other specs in this worker expect the plain panel).
test.describe('tip at checkout (ut-docs#7107)', () => {
  // page.request talks to this worker's own till (the bare `request` fixture
  // does not), so the switch is flipped per test.
  test.beforeEach(async ({ page }) => {
    const res = await page.request.post('/api/settings/payments-tips', { form: { enabled: 'on', presets: '10,15,20' } });
    expect(res.ok()).toBeTruthy();
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset', { headers: { 'HX-Request': 'true' } });
    await page.request.post('/api/settings/payments-tips', { form: { presets: '10,15,20' } }); // no "enabled" = off
  });

  async function openPayment(page: Page) {
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.getByRole('textbox').first().fill('5000000000012'); // Coca-Cola, 1.20
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await page.getByTestId('payment-open').click();
    await expect(page.getByTestId('tip-row')).toBeVisible();
  }

  test('1024x600: five options in one row, method buttons still fully on screen', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await openPayment(page);

    await expect(page.getByTestId('tip-none')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByTestId('tip-preset')).toHaveCount(3);
    // The amount each preset adds: 10/15/20 percent of 1.20, half up.
    await expect(page.getByTestId('tip-preset').nth(0)).toContainText('£0.12');
    await expect(page.getByTestId('tip-preset').nth(1)).toContainText('£0.18');
    await expect(page.getByTestId('tip-preset').nth(2)).toContainText('£0.24');

    const geo = await page.evaluate(() => {
      const box = (e: Element) => e.getBoundingClientRect();
      const opts = [...document.querySelectorAll('#tip-row .tip-opt')].map(box);
      const panel = box(document.getElementById('panel-pay')!);
      const pays = [...document.querySelectorAll('#panel-pay .pay-grid .pay-btn')]
        .filter((e) => (e as HTMLElement).offsetParent !== null).map(box);
      return {
        tops: opts.map((r) => Math.round(r.top)),
        minHeight: Math.min(...opts.map((r) => r.height)),
        panelBottom: panel.bottom,
        payBottoms: pays.map((r) => r.bottom),
        payCount: pays.length,
      };
    });
    expect(new Set(geo.tops).size, 'all tip options share one row at 1024 wide').toBe(1);
    expect(geo.minHeight, 'tip options keep the 44px touch floor').toBeGreaterThanOrEqual(44);
    expect(geo.payCount).toBeGreaterThanOrEqual(2);
    for (const b of geo.payBottoms) {
      expect(b, 'a method button must not be pushed below the panel by the tip row').toBeLessThanOrEqual(geo.panelBottom + 1);
    }
  });

  test('a tip above the bill is stopped before any request; a valid cash tip lands on the receipt', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await openPayment(page);

    let tenders = 0;
    page.on('request', (r) => { if (r.url().includes('/api/pos/tender')) tenders++; });

    await page.getByTestId('tip-custom').click();
    await page.getByTestId('tip-custom-input').fill('5.00');
    await expect(page.getByTestId('tip-total')).toHaveText('The tip cannot be more than the bill.');
    await page.locator('#panel-pay .pay-grid .pay-btn[data-method="cash"]').click();
    await page.waitForTimeout(400);
    expect(tenders, 'an over-bill tip never reaches the server').toBe(0);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('tip-preset').nth(1).click(); // 15% of 1.20 = 0.18
    await expect(page.getByTestId('tip-total')).toHaveText('Total with tip: £1.38');
    await expect(page.locator('#tip-bp')).toHaveValue('1500');
    await page.locator('#panel-pay .pay-grid .pay-btn[data-method="cash"]').click();
    await expect(page.locator('#basket')).toContainText('£1.38'); // receipt: Cash 1.38
    await expect(page.locator('#basket')).toContainText('Tip £0.18');
    expect(tenders).toBe(1);
  });

  test('360px phone: options wrap, nothing overflows sideways', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await openPhoneSheet(page); // the tender panel is in the phone basket sheet
    await page.getByTestId('payment-open').click();
    await expect(page.getByTestId('tip-row')).toBeVisible();
    const geo = await page.evaluate(() => {
      const opts = [...document.querySelectorAll('#tip-row .tip-opt')].map((e) => e.getBoundingClientRect());
      return {
        docWidth: document.documentElement.scrollWidth,
        maxRight: Math.max(...opts.map((r) => r.right)),
        minLeft: Math.min(...opts.map((r) => r.left)),
        minHeight: Math.min(...opts.map((r) => r.height)),
      };
    });
    expect(geo.docWidth, 'no horizontal page overflow').toBeLessThanOrEqual(360);
    expect(geo.maxRight).toBeLessThanOrEqual(360);
    expect(geo.minLeft).toBeGreaterThanOrEqual(0);
    expect(geo.minHeight).toBeGreaterThanOrEqual(44);
  });
});
