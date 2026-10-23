import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3396: a kiosk basket holding an age-restricted item can never be
// paid at the kiosk (ut-docs#3340 fails the POST closed). The payment
// picker now says "pay at the counter" the moment it opens and offers no
// payment method, instead of the customer finding out only after tapping
// one. Driven through the real screens: the item is flagged in the catalog
// editor, the customer adds it at the kiosk and taps Checkout.

const RUN = Date.now().toString(36);
const NAME = `Age3396 ${RUN}`;
const BARCODE = `50099933${Date.now().toString().slice(-6)}`;

test('the kiosk payment picker sends an age-restricted basket to the counter up front', async ({ page, browser }) => {
  test.setTimeout(120_000);
  await page.goto('/catalog');
  await page.locator('#item-form-add-btn').click();
  await page.locator('#item-barcode').fill(BARCODE);
  await page.locator('#item-name').fill(NAME);
  await page.locator('#item-price').fill('4.50');
  await page.locator('#item-age-restricted').check();
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

  for (const vp of [
    { name: 'kiosk-1024x600', width: 1024, height: 600, lang: '' },
    { name: 'phone-360x740', width: 360, height: 740, lang: '' },
    { name: 'kiosk-1024x600-fa', width: 1024, height: 600, lang: 'fa' },
  ]) {
    const ctx = await browser.newContext({ hasTouch: true, viewport: { width: vp.width, height: vp.height } });
    const kiosk = await ctx.newPage();
    const assertClean = watchConsole(kiosk);
    try {
      if (vp.lang) {
        // RTL check: the customer starts the order in Persian (ut-docs#4200).
        await kiosk.goto('/self-order');
        await kiosk.locator(`nav.selforder-langs a[lang="${vp.lang}"]`).click();
        await expect(kiosk.locator('html')).toHaveAttribute('dir', 'rtl');
      }
      // Starting an order (language pick) resets the basket, so add the
      // item afterwards.
      const res = await kiosk.request.post('/api/self-order/scan', { form: { code: BARCODE } });
      expect(res.ok(), 'adding the restricted item to the kiosk basket').toBeTruthy();
      await kiosk.goto('/self-order/shop');
      const cart = kiosk.locator('#selforder-cart');
      await expect(cart).toContainText(NAME);
      await cart.locator('.selforder-checkout').click();

      const modal = kiosk.locator('#selforder-modal');
      await expect(modal).toBeVisible();
      const msg = modal.locator('.payment-picker-blocked');
      await expect(msg).toBeVisible();
      if (!vp.lang) {
        await expect(msg).toContainText('Please pay at the counter');
        await expect(modal.locator('h3')).toHaveText('Pay at the counter');
      }
      await expect(modal.locator('button[name="method"]'), 'no payment method is offered').toHaveCount(0);
      await expect(modal).not.toContainText('No payment methods');
      // The message is readable: inside the viewport, and it covers
      // neither the title nor Cancel (it sits inline, not as a toast).
      const cancel = modal.locator('button[data-action="close-clear:selforder-modal"]');
      await expect(cancel).toBeVisible();
      const box = (await msg.boundingBox())!;
      const title = (await modal.locator('h3').boundingBox())!;
      const cbox = (await cancel.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(vp.width);
      expect(box.y, 'message below the title').toBeGreaterThanOrEqual(title.y + title.height - 1);
      expect(cbox.y, 'Cancel below the message').toBeGreaterThanOrEqual(box.y + box.height - 1);
      await kiosk.screenshot({ path: test.info().outputPath(`picker-${vp.name}.png`), fullPage: false });
      await cancel.click();
      await expect(cart).toContainText(NAME);

      // Leave the walk-up kiosk basket empty for the next test.
      const remove = cart.locator('button[hx-post="/api/self-order/remove"]');
      while (await remove.count()) {
        await Promise.all([
          kiosk.waitForResponse((r) => r.url().includes('/api/self-order/remove')),
          remove.first().click(),
        ]);
      }
      assertClean();
    } finally {
      await ctx.close();
    }
  }
});
