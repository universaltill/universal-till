import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#5823: a successful deposit refund (Pfand) closed the dialog and left
// no confirmation at all. The 2xx response now carries an out-of-band
// .pos-notice.success into #pfand-notice, a live region on the menu page
// OUTSIDE the dialog, so it survives the dialog closing.

// Helpers copied (not shared) from deposit-refund-payout-osk-1249.spec.ts.
async function ensureShiftOpen(page: Page) {
  await page.goto('/shifts');
  const openForm = page.locator('#open-shift-form');
  if (await openForm.count()) {
    await openForm.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  }
}

async function openPfand(page: Page, query = '') {
  await page.goto('/menu' + query);
  await page.waitForSelector('.menu-grid');
  await page.locator('[data-testid="menu-pfand-open"]').click();
  await expect(page.locator('#pfand-modal')).toBeVisible();
}

async function submitPfand(page: Page, amount: string, pin = '1234') {
  await page.locator('#pfand-amount').fill(amount);
  await page.locator('#pfand-modal [name="manager_pin"]').fill(pin);
  const [response] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/shifts/pfandrueckgabe')),
    page.locator('#pfand-modal button[type="submit"]').click(),
  ]);
  return response;
}

test('deposit refund: success shows a page-level confirmation that survives the dialog closing', async ({ page }) => {
  await ensureShiftOpen(page);
  await openPfand(page);
  const response = await submitPfand(page, '2.50');
  expect(response.status()).toBe(200);

  await expect(page.locator('#pfand-modal')).toBeHidden();
  const notice = page.locator('#pfand-notice .pos-notice.success');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('Deposit refund recorded:');
  await expect(notice).toContainText('2.50');
  expect(await notice.evaluate((el) => el.closest('dialog'))).toBeNull();
  expect(await page.locator('dialog:modal').count()).toBe(0);

  const dismiss = notice.locator('.notice-dismiss');
  const box = await dismiss.boundingBox();
  expect(box!.width).toBeGreaterThanOrEqual(44);
  expect(box!.height).toBeGreaterThanOrEqual(44);
  await dismiss.click();
  await expect(page.locator('#pfand-notice .pos-notice')).toHaveCount(0);
});

test('deposit refund: a failing payout keeps the dialog open with the error and no notice', async ({ page }) => {
  await ensureShiftOpen(page);
  await openPfand(page);
  const response = await submitPfand(page, '0');
  expect(response.status()).toBe(400);
  await expect(page.locator('#pfand-modal')).toBeVisible();
  await expect(page.locator('#pfand-result')).toContainText('amount must be greater than zero');
  await expect(page.locator('#pfand-notice .pos-notice')).toHaveCount(0);
});

for (const vp of [
  { width: 1024, height: 600 },
  { width: 360, height: 640 },
]) {
  test(`deposit refund: confirmation is visible at ${vp.width}x${vp.height} without overflow`, async ({ page }) => {
    await page.setViewportSize(vp);
    await ensureShiftOpen(page);
    await openPfand(page);
    expect((await submitPfand(page, '2.50')).status()).toBe(200);
    const notice = page.locator('#pfand-notice .pos-notice.success');
    await expect(notice).toBeInViewport();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    // toBeInViewport is true even when a sticky bar paints over the notice
    // (the 360px phone top bar did), so also assert it is not under the bar
    // and that its dismiss button is what a tap at its centre would hit.
    const bar = await page.evaluate(() => {
      const n = document.querySelector('.nav');
      if (!n || getComputedStyle(n).position !== 'sticky') return 0;
      return n.getBoundingClientRect().bottom;
    });
    const nb = (await notice.boundingBox())!;
    expect(nb.y).toBeGreaterThanOrEqual(bar - 0.5);
    const dBox = (await notice.locator('.notice-dismiss').boundingBox())!;
    const hit = await page.evaluate(({ x, y }) => {
      const el = document.elementFromPoint(x, y);
      return !!el && !!el.closest('.notice-dismiss');
    }, { x: dBox.x + dBox.width / 2, y: dBox.y + dBox.height / 2 });
    expect(hit).toBe(true);
  });
}

test('deposit refund: RTL locale renders the notice with dir=rtl', async ({ page }) => {
  await ensureShiftOpen(page);
  await openPfand(page, '?lang=ar');
  expect((await submitPfand(page, '2.50')).status()).toBe(200);
  const notice = page.locator('#pfand-notice .pos-notice.success');
  await expect(notice).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
});

test('deposit refund: works with the internet unreachable', async ({ page, context }) => {
  await ensureShiftOpen(page);
  await openPfand(page);
  // context.setOffline() also cuts loopback (the till itself); block every
  // non-local origin instead, which is what "offline" means to a till.
  await context.route((url) => !['127.0.0.1', 'localhost'].includes(url.hostname), (route) => route.abort());
  expect((await submitPfand(page, '2.50')).status()).toBe(200);
  await expect(page.locator('#pfand-notice .pos-notice.success')).toBeVisible();
});
