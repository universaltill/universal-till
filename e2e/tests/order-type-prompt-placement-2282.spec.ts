import { test, expect } from './fixtures';
import { setOrderTypePromptMode } from './helpers';

// ut-docs#2282: a setting picks WHEN/WHERE the sale screen asks the cashier
// dine-in or takeaway -- top of basket (today's always-visible toggle, the
// default), before the first item lands in an empty basket, or deferred
// until Pay. ut-docs#2309 (product-owner scope change, outcome 1 taken):
// the per-line Dine-in/Takeaway control this card also removes is covered
// by this file's last test.
//
// The setting is a SERVER-side value shared by every spec on this server
// (fixtures.ts's own "one live till, workers: 1" rule) -- every test here
// restores 'top' in afterEach even on failure, or a failed run leaks
// before_item/at_pay into an unrelated later spec's basket interactions.
test.describe('ut-docs#2282 dine-in/takeaway prompt placement', () => {
  test.afterEach(async ({ page }) => {
    await setOrderTypePromptMode(page, 'top');
    await page.request.post('/api/pos/reset').catch(() => {});
  });

  test('top (default): the basket-top toggle is visible immediately and no intercept modal exists in the flow', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('[data-testid="order-type-dine-in"]')).toBeVisible();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toBeVisible();

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    // The modal must never have been shown (it isn't even open, and no
    // scan was intercepted -- the item landed straight away).
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
  });

  test('before_item: the very first item into an empty basket is intercepted; answering adds it and lifts the gate for the rest of the sale', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();

    // Intercepted: the modal opens and the item has NOT landed yet.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

    // Answer Takeaway -- the item lands, tagged takeaway (the whole-basket
    // toggle reflects the choice).
    await page.getByTestId('order-type-prompt-takeaway').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveClass(/is-active/);

    // A second item on the same (now non-empty, already-answered) basket
    // is NOT intercepted again.
    await page.locator('.scan-row input[name="code"]').fill('5000000000029');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toContainText('Pepsi');
  });

  test('before_item: Cancel closes the modal and leaves the item unadded', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();

    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
  });

  test('at_pay: items add freely with no prompt; the Pay button is intercepted until answered, then opens the payment overlay', async ({ page }) => {
    await setOrderTypePromptMode(page, 'at_pay');
    await page.goto('/');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    // No prompt on the item add itself under this placement.
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.getByTestId('payment-open').click();
    // Intercepted: the modal opens and the payment overlay does NOT.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    await page.getByTestId('order-type-prompt-dine-in').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    // Answered -- the overlay now opens.
    await expect(page.locator('#payment-overlay')).toBeVisible();
  });

  // ut-docs#2309 (product-owner scope change, outcome 1 taken): confirms
  // the removal itself didn't break basket rendering for a plain multi-line
  // basket -- no per-line control markup renders at all any more, and the
  // basket's own columns (qty/price/total/remove) are all still intact.
  test('the removed per-line control leaves basket rendering otherwise intact', async ({ page }) => {
    await page.goto('/');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await page.locator('.scan-row input[name="code"]').fill('5000000000029');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#basket')).toContainText('Pepsi');

    await expect(page.locator('[class*="line-order-type"]')).toHaveCount(0);
    await expect(page.locator('[data-testid^="line-order-type-"]')).toHaveCount(0);
    await expect(page.locator('.order-type-mixed')).toHaveCount(0);

    // Basket-top bulk toggle is still there and still works.
    await expect(page.locator('[data-testid="order-type-dine-in"]')).toBeVisible();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toBeVisible();
    await expect(page.locator('tbody#basket-lines tr')).toHaveCount(2);
  });
});
