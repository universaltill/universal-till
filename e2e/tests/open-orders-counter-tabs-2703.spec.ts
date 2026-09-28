import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { execFileSync } from 'child_process';
import { drainParkedOrders, watchConsole } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#2703 (reopened), product owner: "the open orders need to have 2
// sections or tabs, for open orders and pay on the counter, when the
// cashier clicks on them both should act the same: the order shows open on
// the sell screen same as a normal order and cashier can get the payment or
// change it." And a pay-at-the-counter order on the pilot tablet (C-2,
// "Avocado Lachs Bagel × 1, Black Shadow × 1", "22258 min") offered only
// "Mark collected" -- "no one can get that payment".
//
// Driven through the real screens on a real till: a till Hold lands under
// On hold, never under Pay at the counter; a legacy counter order (written
// the way the kiosk stored it before counter orders were held sales -- no
// prices; e2e/counter_order_seed, since no code path makes one any more)
// opens on the sale screen priced from the catalogue, with the line that
// matches nothing called out to add by hand, and is paid as a normal sale;
// and the sale screen's popup carries the same two tabs.
//
// HONESTY NOTE: Chromium; the pilot tablet's WebView is the hardware lane's.

test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
});

function seedLegacyCounterOrder(...lines: [string, number][]): { id: string; display_no: string } {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  const args = lines.flatMap(([name, qty]) => [name, String(qty)]);
  const out = execFileSync('go', ['run', './e2e/counter_order_seed', ...args], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  // db.Open logs to stdout too; the order is the one JSON line.
  const json = out.toString().split('\n').reverse().find((l) => l.trim().startsWith('{'));
  if (!json) throw new Error(`counter_order_seed printed no order: ${out.toString()}`);
  return JSON.parse(json);
}

async function holdACoke(page: Page, label: string) {
  await page.goto('/');
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

test('a till hold is under On hold; a legacy counter order opens priced, flags what to add by hand, and is paid', async ({ page }) => {
  test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir); a reused server has none');
  const assertClean = watchConsole(page);
  await drainParkedOrders(page.request);
  await holdACoke(page, 'Window seat');
  const legacy = seedLegacyCounterOrder(['coca-cola can 330ML', 2], ['Black Shadow', 1]);

  // On hold (the default while it has something): the till's own hold only.
  await page.goto('/open-orders');
  const holdTab = page.getByTestId('open-orders-tab-hold');
  const counterTab = page.getByTestId('open-orders-tab-counter');
  await expect(holdTab).toHaveAttribute('aria-current', 'page');
  await expect(holdTab).toContainText('1');
  await expect(counterTab).toContainText('1');
  const rows = page.getByTestId('open-order-row');
  await expect(rows.filter({ hasText: 'Window seat' })).toHaveCount(1);
  await expect(rows.filter({ hasText: legacy.display_no })).toHaveCount(0);

  // Pay at the counter: the legacy order, with what was ordered, no Mark collected.
  await counterTab.click();
  await expect(page).toHaveURL(/tab=counter/);
  await expect(counterTab).toHaveAttribute('aria-current', 'page');
  const row = rows.filter({ hasText: legacy.display_no });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText('Black Shadow');
  await expect(rows.filter({ hasText: 'Window seat' })).toHaveCount(0);
  await expect(page.locator('body')).not.toContainText('Mark collected');

  // Tap it: the sale screen, the matched line at today's price, and the
  // unmatched line named so the cashier can ring it up by hand.
  await row.click();
  await expect(page).not.toHaveURL(/unmatched=/);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('2.40');
  const notice = page.getByTestId('counter-unmatched-notice');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('Black Shadow × 1');
  await expect(notice).not.toContainText('Coca-Cola');

  // The list travels with the order (review finding): park it, open it
  // again from the popup -- still there; and a reload keeps it.
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(notice).toHaveCount(0);
  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await modal.getByTestId('parked-orders-tab-counter').click();
  await modal.locator('.parked-order', { hasText: legacy.display_no }).click();
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('Black Shadow × 1');
  await page.reload();
  await expect(notice).toBeVisible();
  // Dismissed: gone for good for this sale.
  await notice.getByRole('button').click();
  await expect(notice).toHaveCount(0);
  await page.reload();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(notice).toHaveCount(0);

  // Paid like any sale; gone from both tabs.
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await page.goto('/open-orders?tab=counter');
  await expect(page.getByTestId('open-order-row').filter({ hasText: legacy.display_no })).toHaveCount(0);
  await expect(page.getByTestId('open-orders-empty')).toBeVisible();
  // And the paid sale carries the customer's own number.
  await page.goto('/orders');
  await expect(page.locator('body')).toContainText(legacy.display_no);
  assertClean();
});

test('the sale screen popup has the same two tabs, and a counter order opens from it', async ({ page }) => {
  test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir); a reused server has none');
  const assertClean = watchConsole(page);
  await drainParkedOrders(page.request);
  await holdACoke(page, 'Bar stool');
  const legacy = seedLegacyCounterOrder(['Coca-Cola Can 330ml', 1]);
  await page.reload();

  await page.locator('.tender-default-footer [data-testid="parked-orders-open"]').click();
  const modal = page.locator('#parked-orders-modal');
  await expect(modal).toBeVisible();
  const holdTab = modal.getByTestId('parked-orders-tab-hold');
  const counterTab = modal.getByTestId('parked-orders-tab-counter');
  await expect(holdTab).toHaveAttribute('aria-selected', 'true');
  await expect(modal.locator('.parked-order', { hasText: 'Bar stool' })).toBeVisible();
  await expect(modal.locator('.parked-order', { hasText: legacy.display_no })).toHaveCount(0);

  await counterTab.click();
  await expect(counterTab).toHaveAttribute('aria-selected', 'true');
  await expect(counterTab).toBeFocused();
  // WAI-ARIA tabs: arrow keys switch tabs (and wrap), focus follows.
  await page.keyboard.press('ArrowLeft');
  await expect(holdTab).toHaveAttribute('aria-selected', 'true');
  await expect(holdTab).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(counterTab).toHaveAttribute('aria-selected', 'true');
  await expect(counterTab).toBeFocused();
  await expect(modal.locator('.parked-order', { hasText: 'Bar stool' })).toHaveCount(0);
  const counterRow = modal.locator('.parked-order', { hasText: legacy.display_no });
  await expect(counterRow).toBeVisible();

  // The same action as any row: the order opens on the sale screen. Every
  // line matched, so there is nothing to add by hand.
  await counterRow.click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.20');
  await expect(page.getByTestId('counter-unmatched-notice')).toHaveCount(0);
  assertClean();
});
