import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2282: sale.order_type_prompt_stage — WHERE the cashier is asked
// for the sale-level dine-in/takeaway order type. Three modes:
//   cart_top (default)  — unchanged: the basket-top toggle, always visible.
//   before_sale         — a modal on the first add to an empty basket; the
//                          basket-top toggle still shows afterward.
//   at_pay               — no basket-top toggle at all; the modal opens on
//                          Pay, and the tender screen (#payment-overlay)
//                          only opens once it's answered. Cancel returns to
//                          the basket unchanged.
//
// Settings/UI-scale precedent (settings-fee-row-251.spec.ts): the stage is
// set/restored via a direct API request, not the Settings UI's own
// select+submit flow — this suite's till is a single shared server
// (playwright.config.ts, workers: 1), so every test restores cart_top
// afterward regardless of pass/fail.
//
// Assertions target `.order-type-toggle-group` specifically, not the wider
// `.order-type-row` — that row also hosts the unrelated Table-assignment
// button (#table-picker, basket.html/ADR-0054), which at_pay mode's CSS
// (app.css) deliberately leaves alone.
//
// Coca-Cola (barcode 5000000000012) is the same item every other basket
// spec in this suite scans (hold-named-tab.spec.ts, etc.) — no fixture
// setup needed, it's part of the default e2e seed.

async function setStage(page, stage: string) {
  await page.request.post('/api/settings/sale-order-type-prompt', { form: { stage } });
}

test.afterEach(async ({ page }) => {
  await setStage(page, 'cart_top');
  await page.request.post('/api/pos/reset').catch(() => {});
});

test.describe('order-type prompt stage (ut-docs#2282)', () => {
  test('cart_top (default): basket-top toggle shows, no modal on add or Pay', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setStage(page, 'cart_top');
    await page.goto('/');

    await expect(page.locator('.order-type-toggle-group')).toBeVisible();

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#order-type-modal')).toBeHidden();

    await page.locator('.payment-trigger').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    assertClean();
  });

  test('before_sale: modal prompts on the first add to an empty basket, not again after', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setStage(page, 'before_sale');
    await page.goto('/');

    // Basket starts empty (afterEach reset) -- the first add opens the modal
    // instead of landing immediately.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-modal')).toBeVisible();
    // The intercepted add hasn't landed yet.
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

    await page.locator('[data-testid="order-type-modal-takeaway"]').click();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    // Answering the modal resumes the original add.
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    // The basket-top toggle still renders and reflects the answer.
    await expect(page.locator('.order-type-toggle-group')).toBeVisible();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveClass(/is-active/);

    // A second add (basket no longer empty) does not ask again.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    assertClean();
  });

  test('before_sale: cancelling the prompt leaves the basket empty', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setStage(page, 'before_sale');
    await page.goto('/');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-modal')).toBeVisible();

    await page.locator('[data-testid="order-type-modal-cancel"]').click();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    assertClean();
  });

  test('at_pay: no basket-top toggle; Pay opens the modal, tender opens only after an answer', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setStage(page, 'at_pay');
    await page.goto('/');

    await expect(page.locator('.order-type-toggle-group')).toBeHidden();

    // Adding items never prompts in this mode.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#order-type-modal')).toBeHidden();

    // Pay opens the modal first, not the tender overlay.
    await page.locator('.payment-trigger').click();
    await expect(page.locator('#order-type-modal')).toBeVisible();
    await expect(page.locator('#payment-overlay')).toBeHidden();

    await page.locator('[data-testid="order-type-modal-dine-in"]').click();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    // Answering opens the tender screen.
    await expect(page.locator('#payment-overlay')).toBeVisible();
    assertClean();
  });

  test('at_pay: cancelling the Pay-time prompt returns to the basket unchanged', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setStage(page, 'at_pay');
    await page.goto('/');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.locator('.payment-trigger').click();
    await expect(page.locator('#order-type-modal')).toBeVisible();

    await page.locator('[data-testid="order-type-modal-cancel"]').click();
    await expect(page.locator('#order-type-modal')).toBeHidden();
    await expect(page.locator('#payment-overlay')).toBeHidden();
    // The basket itself is untouched.
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    assertClean();
  });
});
