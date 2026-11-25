import { test, expect } from './fixtures';

// ut-docs#7045: the Refund button is disabled while its POST /api/refund is
// in flight (a double tap), re-enabled on the response, and a quantity edit
// (which fires /api/refund/preview from inside the form) never greys it out.
// The server-side guard is the guarantee; this pins the client half.

test('refund form: Refund is disabled while the refund is being sent (ut-docs#7045)', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await page.locator('button[data-action*="go:/refund/"]').click();
  await expect(page.locator('#refund-form')).toBeVisible();

  const submit = page.locator('#refund-submit');
  await expect(submit).toBeEnabled();

  // A preview request inside the form must not disable Refund. The preview is
  // held open while we assert, so a fast answer cannot hide a disabled flash
  // (tester, ut-docs#7045: without the hold this step passed with
  // hx-disinherit removed).
  const qty = page.locator('#refund-form input[name^="qty_"]').first();
  await expect(qty).toHaveCount(1);
  let releasePreview!: () => void;
  const previewHeld = new Promise<void>((r) => (releasePreview = r));
  let previewSeen!: () => void;
  const previewStarted = new Promise<void>((r) => (previewSeen = r));
  await page.route('**/api/refund/preview', async (route) => {
    previewSeen();
    await previewHeld;
    await route.continue();
  });
  await qty.fill('1');
  await previewStarted;
  await expect(submit).toBeEnabled();
  releasePreview();

  // Hold the POST open until the test releases it.
  let release!: () => void;
  const held = new Promise<void>((r) => (release = r));
  let posts = 0;
  await page.route('**/api/refund', async (route) => {
    posts++;
    await held;
    await route.fulfill({ status: 409, contentType: 'text/plain', body: 'held' });
  });

  await submit.click();
  await expect(submit).toBeDisabled();
  release();
  await expect(submit).toBeEnabled();
  expect(posts).toBe(1);
});
