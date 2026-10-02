import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3357: the product owner (iPhone, TestFlight) reported /orders as
// "a grid view and not good for a mobile phone". ut-docs#3359's generic
// table→card mechanism (web/public/table-cards.js + app.css `table.t-cards`)
// covers /orders automatically — it names "order status" explicitly in its
// own ask — but neither that PR's own e2e coverage nor the general
// phone-layout-sweep-3297 sweep (which visits /orders with an EMPTY board,
// so the table never even renders — order_status.go's `{{ if not .Orders }}`
// branch) drives a REAL order through it. This is that missing coverage:
// ring up a real sale, so a real <tr> lands in the table, and prove it
// renders as a labelled card and that the one-tap status buttons —
// including the terminal-row OOB delete (order_status.go's
// writeOrderStatusFragment, ut-docs#1389) — still work once the row is a
// card instead of a <tr> laid out as a table row.
test.describe('phone 360px: /orders renders a real order as a card (ut-docs#3357)', () => {
  test.use({ hasTouch: true, isMobile: true });

  test('a real order is a labelled card, not a sideways-scrolling row, and one-tap actions still work', async ({ page }) => {
    const assertClean = watchConsole(page);
    // Ring up at the default (desktop) viewport, same as
    // orders-terminal-row-removal-1389.spec.ts — the phone sale screen
    // (#3059) is a different layout and isn't what this card is about;
    // only /orders itself needs to be driven at the phone tier.
    await page.goto('/');
    await page.waitForSelector('.pos-container');

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

    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/orders');
    const table = page.locator('#orders table.table');
    await expect(table).toHaveClass(/\bt-cards\b/);

    const row = page.locator(`tr#order-row-${receiptNo}`);
    await expect(row).toBeVisible();
    await expect(row).toHaveClass(/\bt-cards-row\b/);

    // The card is never wider than the phone, and nothing on the page
    // scrolls sideways to read it (the whole point of ut-docs#3359).
    const box = await row.boundingBox();
    expect(box!.width).toBeLessThanOrEqual(360);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(361);

    // Order # is the card's title: table-cards.js still sets its
    // data-label (every labelled cell gets one), but .t-cards-title's own
    // CSS rule deliberately excludes it from the ::before label rendering
    // that every OTHER labelled cell gets (app.css: `[data-label]:not(.t-cards-title)::before`)
    // — it must read as a title, not as a "Order #: 123" pair.
    const title = row.locator('.t-cards-title');
    await expect(title).toBeVisible();
    expect((await title.textContent())?.trim().length).toBeGreaterThan(0);
    expect(await title.evaluate((el) => getComputedStyle(el, '::before').content)).toBe('none');

    // Status carries its header as a REAL rendered ::before label (not just
    // a present-but-invisible data-label attribute — a mutation test
    // confirmed the earlier version of this spec still passed with the
    // label's content stripped, which is exactly the unreadable-card
    // failure ut-docs#3357 is about) and still shows the status chip.
    const statusCell = row.locator('td[data-label="Status"]');
    await expect(statusCell).toBeVisible();
    expect(await statusCell.evaluate((el) => getComputedStyle(el, '::before').content)).toBe('"Status"');
    await expect(statusCell.locator('.order-status')).toBeVisible();

    // Tap a one-tap status button for real (not an API call) — operable,
    // not just readable, is this card's acceptance criterion. Every action
    // button must sit INSIDE the card's own box: the Actions column has a
    // real header ("Update"), so its buttons render through the page's own
    // .btn-actions div, not .t-cards-actions (table-cards.js only gives
    // that class to a header-less cell) — .btn-actions's base rule has no
    // flex-wrap, so at 360px four buttons spilled past the card border on
    // both sides until app.css gained a `table.t-cards .btn-actions`
    // override.
    const preparing = row.locator('button', { hasText: 'Preparing' });
    await expect(preparing).toBeVisible();
    const rowBoxBefore = (await row.boundingBox())!;
    for (const btn of await row.locator('.btn-actions button').all()) {
      const b = (await btn.boundingBox())!;
      expect(b.x).toBeGreaterThanOrEqual(rowBoxBefore.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(rowBoxBefore.x + rowBoxBefore.width + 1);
    }
    await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/orders/${receiptNo}/status`)),
      preparing.click(),
    ]);
    await expect(statusCell.locator('.order-status')).toHaveAttribute('data-status', 'preparing');
    // Still a readable card after the htmx swap, not reverted to a bare row.
    await expect(row).toHaveClass(/\bt-cards-row\b/);

    // Advance to the terminal status: the OOB "delete" (ut-docs#1389) must
    // still remove the card by id even though it's now styled as a card,
    // not a plain table row.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/orders/${receiptNo}/status`)),
      row.locator('button', { hasText: 'Collected' }).click(),
    ]);
    await expect(row).toHaveCount(0);

    assertClean();
  });
});
