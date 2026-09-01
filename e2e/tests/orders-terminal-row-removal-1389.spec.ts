import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1389: /orders is the ACTIVE work queue. Marking an order
// Collected (or Cancelled) must remove its row from the board immediately
// — no full-page reload, and it must not resurface on the next poll. This
// is genuinely client-observable behavior (an htmx out-of-band "delete"
// swap, order_status.go's writeOrderStatusFragment) that a rendered-HTML-
// string assertion can prove the SERVER sent, but not that a real browser
// actually acted on it — so this drives a real sale to completion, taps
// the button for real, and reads the live DOM (tester skill: "look at it,
// don't just assert on it").
test.describe('orders board removes a terminal order immediately (ut-docs#1389)', () => {
  test('marking an order Collected removes its row without a page reload, and it stays gone on refresh', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // Ring up one real sale, same barcode/flow as tender-panel-reachable.spec.ts.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('.tab-panel .btn', { hasText: 'Cash' }).first().click(),
    ]);
    const receiptHeading = await page.locator('#basket.receipt-view h2', { hasText: 'Receipt' }).textContent();
    const receiptNo = receiptHeading?.match(/#(\S+)/)?.[1];
    expect(receiptNo, `receipt number must be readable from ${receiptHeading}`).toBeTruthy();

    await page.goto('/orders');
    const row = page.locator(`#order-row-${receiptNo}`);
    await expect(row).toBeVisible();

    // Tap Collected — a real click on the real button, not an API call, so
    // this exercises the actual htmx swap a shop floor operator triggers.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/orders/${receiptNo}/status`)),
      row.locator('button', { hasText: 'Collected' }).click(),
    ]);
    // The row must be gone from the live DOM right away — not merely
    // hidden, and not waiting for the 15s poll (poll interval means a
    // flaky sleep-based wait would either be too slow or hide a real bug;
    // toHaveCount(0) polls until it's actually removed or times out).
    await expect(row).toHaveCount(0);

    // Reloading the whole page (a fresh GET /orders, not the fragment
    // poll) must not bring it back either — proves the server-side filter,
    // not just the client-side swap, actually excludes it now.
    await page.reload();
    await expect(page.locator(`#order-row-${receiptNo}`)).toHaveCount(0);

    assertClean();
  });
});
