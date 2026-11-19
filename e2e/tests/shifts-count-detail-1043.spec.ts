import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#1043: a closed shift's stored denomination count is readable again
// from the Shifts history ("Show count"). The Go tests prove the route, the
// parser and the read-only guarantee; only a browser proves the htmx wiring:
// the row button loads the shared panel, Hide empties it and keeps focus on
// the heading, an unreachable till lands the error inline (XR_TARGETS), and
// the button is a real touch target with no page scroll at phone width.
//
// Every test establishes its own state: the worker till is shared by the
// specs a worker runs.

async function closeFreshShift(page: Page, withCount: boolean): Promise<string> {
  await page.goto('/shifts');
  if ((await page.locator('#close-shift-form').count()) === 0) {
    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
  }
  if (withCount) {
    await page.locator('#close-shift-form details.catalog-extra > summary').first().click();
    await page.locator('#denom-grid [data-denom="1000"]').fill('1');
    await page.locator('#denom-grid [data-denom="1000"]').dispatchEvent('change');
  }
  // Rows opened in the same second tie on opened_at, so address the row by id.
  const id = await page.locator('#close-shift-form input[name=shift_id]').inputValue();
  await page.locator('#closing-cash').fill('10.00');
  await page.locator('#close-shift-form button[type=submit]').click();
  await expect(page.locator('#shift-result')).toContainText('Shift closed');
  // the region refresh re-renders the history with the new closed row first
  await expect(page.locator('#open-shift-form')).toBeVisible();
  return id;
}

const showCountFor = (page: Page, id: string) => page.locator(`button[hx-get="/ui/shifts/${id}/count"]`);

test('shift count: Show count loads the stored lines, Hide empties the panel', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 600 });
  const id = await closeFreshShift(page, true);
  await expect(page.locator('#shift-count-title')).toBeVisible();
  await expect(page.locator('#shift-count-body')).toBeEmpty();
  await showCountFor(page, id).click();
  const body = page.locator('#shift-count-body');
  await expect(body.locator('tbody tr')).toHaveCount(1);
  await expect(body.locator('tbody tr').first()).toContainText('£10.00');
  await expect(body.locator('tfoot')).toContainText('Total of the count');
  await expect(body.locator('tfoot')).toContainText('£10.00');
  await expect(body).not.toContainText('differs from this piece count');
  await expect(page.locator('#shift-count-title')).toBeFocused();
  await body.getByRole('button', { name: 'Hide' }).click();
  await expect(body).toBeEmpty();
  await expect(page.locator('#shift-count-title')).toBeFocused();
});

test('shift count: a shift closed without a count says so', async ({ page }) => {
  const id = await closeFreshShift(page, false);
  await showCountFor(page, id).click();
  await expect(page.locator('#shift-count-body')).toContainText('No denomination count was recorded');
  await expect(page.locator('#shift-count-body table')).toHaveCount(0);
});

test('shift count: an unreachable till shows the error inline, in view', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);
  const id = await closeFreshShift(page, false);
  await page.route('**/ui/shifts/*/count', (route) => route.abort());
  const btn = showCountFor(page, id);
  await btn.click();
  const alert = page.locator('#shift-count-body [role="alert"]');
  await expect(alert).toContainText('Something went wrong');
  await expect(alert).toBeInViewport();
  await expect(btn).toBeEnabled();
  assertClean();
});

test('shift count: phone width, the button is a touch target and nothing scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  const id = await closeFreshShift(page, true);
  // The close form's region refresh swaps the history in without re-running
  // table-cards.js (pre-existing: the card layout is applied on load), so
  // measure the page as an operator sees it after the next load.
  await page.reload();
  const btn = showCountFor(page, id);
  await expect(btn).toBeVisible();
  const box = await btn.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  await btn.click();
  await expect(page.locator('#shift-count-body tbody tr')).toHaveCount(1);
  const scroll = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scroll[0]).toBeLessThanOrEqual(scroll[1]);
});
