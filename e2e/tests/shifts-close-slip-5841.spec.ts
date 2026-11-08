import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5841: "Print shift slip" on the close result. The Go handler tests
// prove the route, the gate, the paper and the audit row; only a browser
// proves the htmx wiring: the button inside the close fragment survives the
// close form's refresh-region swap and still posts, the answer lands inline
// in #shift-slip-print-msg (worker tills have no printer, so the real
// answer is the 502 "Print failed" text), a dropped connection lands inline
// too (the XR_TARGETS entry), and the button is a real touch target with no
// page scroll at 1024x600.
//
// Every test establishes its own state: the worker till is shared by the
// specs a worker runs.

async function closeFreshShift(page: Page) {
  await page.goto('/shifts');
  if ((await page.locator('#close-shift-form').count()) === 0) {
    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
  }
  await page.locator('#closing-cash').fill('10.00');
  await page.locator('#skim-pounds').fill('4.00');
  await page.locator('#close-shift-form button[type=submit]').click();
  await expect(page.locator('#shift-result')).toContainText('Shift closed');
}

test('shift slip: the button survives the refresh and the answer lands inline', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  await closeFreshShift(page);
  // the region refresh swapped the open form in; the close result stayed
  await expect(page.locator('#open-shift-form')).toBeVisible();
  const btn = page.locator('#shift-slip-print');
  await expect(btn).toBeVisible();
  await expect(btn).toHaveAttribute('hx-post', /^\/api\/shifts\/[^/]+\/close-slip\/print$/);
  const box = await btn.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  const scroll = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scroll[0]).toBeLessThanOrEqual(scroll[1]);

  let posts = 0;
  page.on('request', (r) => { if (r.url().includes('/close-slip/print') && r.method() === 'POST') posts++; });
  await btn.click();
  // no printer on a worker till: the real server's 502 text, inline
  await expect(page.locator('#shift-slip-print-msg')).toContainText('Print failed');
  expect(posts).toBe(1);
  await expect(btn).toBeEnabled();
  await expect(page.locator('#shift-result')).toContainText('Shift closed');
});

test('shift slip: success text from the server lands inline, no page refresh', async ({ page }) => {
  await closeFreshShift(page);
  await page.route('**/close-slip/print', (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: "<span class='x-report-print-ok' role='status'>✓ Shift slip sent to the printer</span>" }));
  await page.locator('#shift-slip-print').click();
  await expect(page.locator('#shift-slip-print-msg')).toContainText('Shift slip sent to the printer');
  await expect(page.locator('#shift-slip-print')).toBeVisible();
});

test('shift slip: a dropped connection shows inline, not a dead button', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);
  await closeFreshShift(page);
  await page.route('**/close-slip/print', (route) => route.abort());
  await page.locator('#shift-slip-print').click();
  await expect(page.locator('#shift-slip-print-msg [role="alert"]')).toContainText('Something went wrong');
  await expect(page.locator('#shift-slip-print')).toBeEnabled();
  assertClean();
});
