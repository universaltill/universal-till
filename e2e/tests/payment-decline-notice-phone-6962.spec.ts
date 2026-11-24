import { test, expect, Page } from './fixtures';

// ut-docs#6962: a refused card/plugin tender must answer INSIDE the payment
// overlay. At <= 900px the overlay covers #pos-alert and the basket toast, so
// the cashier saw nothing. The server mapping (decline_code -> message) is
// pinned by Go tests; here /api/pos/tender is stubbed.
const BARCODE = '5000000000012';
const DECLINED = 'Payment declined — try again or choose another method';
const NO_READER = 'No card reader is set up on this till. Nothing was charged. Choose another payment method, or ask a manager to set up a card reader.';

for (const vp of [{ width: 390, height: 844 }, { width: 1280, height: 800 }]) {
  test.describe(`payment decline notice ${vp.width}x${vp.height} (ut-docs#6962)`, () => {
    test.use({ viewport: vp });
    test.beforeEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });
    test.afterEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });

    async function openOverlay(page: Page) {
      // Seed the basket through the API: the phone layout tucks the scan row away.
      await page.request.post('/api/pos/scan', { form: { code: BARCODE } });
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      const bar = page.locator('.basket-phonebar');
      if (await bar.isVisible()) await bar.click(); // phone: the basket is a sheet
      await expect(page.locator('#basket')).toContainText('Coca-Cola');
      await page.getByTestId('payment-open').click();
      await expect(page.locator('#payment-overlay')).toHaveAttribute('open', '');
    }
    const decline = (text: string) => (route) =>
      route.fulfill({ status: 402, contentType: 'text/plain; charset=utf-8', body: text + '\n' });
    const pay = (page: Page) => page.locator('#payment-overlay').getByTestId('pay-default').click();

    async function expectTopmost(page: Page) {
      const notice = page.getByTestId('payment-notice');
      await expect(notice).toBeVisible({ timeout: 2000 });
      const top = await notice.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!hit && el.contains(hit);
      });
      expect(top, 'notice is the topmost element at its centre').toBe(true);
      await expect(page.locator('#payment-overlay')).toHaveAttribute('open', '');
      await expect(page.locator('#pos-alert')).toBeHidden();
    }

    test('a declined tender shows the notice in the overlay; dismiss, then a second decline shows it again', async ({ page }) => {
      await openOverlay(page);
      await page.route('**/api/pos/tender', decline(DECLINED));
      await pay(page);
      await expectTopmost(page);
      await expect(page.getByTestId('payment-notice')).toContainText(DECLINED);

      await page.getByTestId('payment-notice').locator('.notice-dismiss').click();
      await expect(page.getByTestId('payment-notice')).toBeHidden();

      await pay(page);
      await expectTopmost(page);
      await expect(page.getByTestId('payment-notice')).toContainText(DECLINED);
    });

    test('the no-reader message is shown as given', async ({ page }) => {
      await openOverlay(page);
      await page.route('**/api/pos/tender', decline(NO_READER));
      await pay(page);
      await expectTopmost(page);
      await expect(page.getByTestId('payment-notice')).toContainText(NO_READER);
    });

    test('a 200 answer carrying an error toast is shown in the overlay', async ({ page }) => {
      await openOverlay(page);
      await page.route('**/api/pos/tender', (route) => route.fulfill({
        status: 200, contentType: 'text/html; charset=utf-8',
        body: '<div id="basket"><div class="pos-notice error" id="toast-message" role="alert"><span class="notice-text">Stub tender refusal</span></div></div>',
      }));
      await pay(page);
      await expectTopmost(page);
      await expect(page.getByTestId('payment-notice')).toContainText('Stub tender refusal');
    });

    test('cash still works after a decline and the notice is gone on the next open', async ({ page }) => {
      await openOverlay(page);
      await page.route('**/api/pos/tender', decline(DECLINED));
      await pay(page);
      await expectTopmost(page);
      await page.unroute('**/api/pos/tender');
      await pay(page);
      await expect(page.locator('#basket.receipt-view')).toBeVisible({ timeout: 15_000 });
      await expect(page.getByTestId('payment-notice')).toBeHidden();
    });
  });
}
