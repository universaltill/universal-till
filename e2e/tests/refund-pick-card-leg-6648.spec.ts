import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { startWorkerTill, type WorkerTill } from './worker-till';

// ut-docs#6648: on a split-bill sale the refund screen asks which card payment
// the refund goes back to. Own till, seeded by e2e/seed_split_card_sale: one
// sale paid with two traced `card` payments (120 and 220). The refund is of
// the 220 item (qty_1) so only the second payment can take it.
test.describe.configure({ mode: 'serial' });

let till: WorkerTill;
let context: BrowserContext;
let page: Page;
let receipt = '';
let refundPosts = 0;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000);
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_split_card_sale'] });
  if (!till.dataDir) return;
  receipt = fs.readFileSync(path.join(till.dataDir, 'e2e-split-card-receipt.txt'), 'utf8').trim();
  context = await browser.newContext({ baseURL: till.url });
  page = await context.newPage();
  page.on('request', (r) => {
    if (r.url().endsWith('/api/refund') && r.method() === 'POST') refundPosts++;
  });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

test('refund asks which card payment; refunding payment 2 leaves payment 1 untouched', async () => {
  test.skip(!till?.dataDir, 'needs a till this spec started');

  await page.goto(`/refund/${receipt}`);
  const group = page.getByTestId('refund-legs');
  await expect(group).toBeHidden();
  await page.selectOption('select[name=method]', 'card');
  await expect(group).toBeVisible();
  await expect(page.getByTestId('refund-leg-option')).toHaveCount(2);
  await expect(page.getByTestId('refund-leg-all')).toBeVisible();
  await expect(group.locator('input[name=refund_leg]:checked')).toHaveCount(0);

  // Refund only the second item; no pick yet: native validity blocks the submit.
  await page.locator('input[name=qty_0]').fill('0');
  await page.locator('#refund-form button[type=submit]').click();
  await expect(group).toBeVisible();
  expect(refundPosts).toBe(0);

  // Pick payment 2 and refund.
  await page.getByTestId('refund-leg-option').nth(1).locator('input').check();
  await page.locator('#refund-form button[type=submit]').click();
  await page.waitForURL(/\/journal\//);
  expect(refundPosts).toBe(1);

  // Reopen: payment 2 is used up (disabled), payment 1 unchanged.
  await page.goto(`/refund/${receipt}`);
  await page.selectOption('select[name=method]', 'card');
  const opts = page.getByTestId('refund-leg-option');
  await expect(opts.nth(1)).toHaveAttribute('data-remaining', '0');
  await expect(opts.nth(1).locator('input')).toBeDisabled();
  await expect(opts.nth(0)).toHaveAttribute('data-remaining', '120');

  // Back to cash: the picker goes away.
  await page.selectOption('select[name=method]', 'cash');
  await expect(group).toBeHidden();
});
