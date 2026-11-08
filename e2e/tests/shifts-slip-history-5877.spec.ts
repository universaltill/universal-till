import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5877: "Print slip" on closed History rows of /shifts. The Go tests
// prove the route, the reprint marker, the audit row and the per-row PIN
// target; only a browser proves the htmx wiring: the row button posts for its
// own shift, the answer (worker tills have no printer, so the real answer is
// the 502 "Print failed" text) lands in that row's own region, a dropped
// connection lands there too (the data-xr-target handler match), an open row
// has no button, and the button is a real touch target with no page scroll
// at 1024x600. The 5841 spec (close-result button) stays unchanged.
//
// Every test establishes its own state: the worker till is shared.

async function closeFreshShift(page: Page) {
  await page.goto('/shifts');
  if ((await page.locator('#close-shift-form').count()) === 0) {
    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
  }
  await page.locator('#closing-cash').fill('10.00');
  await page.locator('#close-shift-form button[type=submit]').click();
  await expect(page.locator('#shift-result')).toContainText('Shift closed');
  await page.goto('/shifts'); // the History rows are server-rendered
}

test('slip history: closed row button posts for its own shift and answers in its own row', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await closeFreshShift(page);
  const btn = page.locator('.shift-slip-row-print').first();
  await expect(btn).toBeVisible();
  await expect(btn).toHaveAttribute('hx-post', /^\/api\/shifts\/[^/]+\/close-slip\/print$/);
  const id = await btn.getAttribute('data-shift-id');
  expect(id).toBeTruthy();
  const box = await btn.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  const scroll = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scroll[0]).toBeLessThanOrEqual(scroll[1]);

  const bodies: string[] = [];
  const urls: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/close-slip/print') && r.method() === 'POST') {
      urls.push(r.url());
      bodies.push(r.postData() || '');
    }
  });
  await btn.click();
  await expect(page.locator(`#shift-slip-print-msg-${id}`)).toContainText('Print failed');
  expect(urls).toHaveLength(1);
  expect(urls[0]).toContain(`/api/shifts/${id}/close-slip/print`);
  expect(bodies[0]).toContain('reprint=1');
  await expect(btn).toBeEnabled();
  // no other row's region was touched
  const filled = await page.evaluate(
    () => Array.from(document.querySelectorAll('.shift-slip-row .x-report-print-msg')).filter((e) => (e.textContent || '').trim() !== '').length);
  expect(filled).toBe(1);
});

test('slip history: a dropped connection shows inline in that row', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);
  await closeFreshShift(page);
  await page.route('**/close-slip/print', (route) => route.abort());
  const btn = page.locator('.shift-slip-row-print').first();
  const id = await btn.getAttribute('data-shift-id');
  await btn.click();
  await expect(page.locator(`#shift-slip-print-msg-${id} [role="alert"]`)).toContainText('Something went wrong');
  await expect(btn).toBeEnabled();
  assertClean();
});

test('slip history: an open shift row has no Print slip button', async ({ page }) => {
  await closeFreshShift(page);
  await page.locator('#opening-cash').fill('10.00');
  await page.locator('#open-shift-form button[type=submit]').click();
  await expect(page.locator('#close-shift-form')).toBeVisible();
  await page.goto('/shifts');
  const openRow = page.locator('table.table tbody tr', { has: page.locator('.tag') }).first();
  await expect(openRow).toBeVisible();
  await expect(openRow.locator('.shift-slip-row-print')).toHaveCount(0);
  await expect(page.locator('.shift-slip-row-print').first()).toBeVisible();
});
