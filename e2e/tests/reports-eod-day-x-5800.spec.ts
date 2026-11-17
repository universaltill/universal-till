import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#5800: the till-wide "day so far" X report on Reports -> End of day.
// The Go tests prove the figures, gate and audit; only a browser proves the
// htmx wiring: Show fills the paper preview, Hide removes it from the DOM, the
// block fits a phone with no page-level horizontal scroll, and a failed print
// lands inline while the preview stays. Touch is emulated by viewport only.

async function openEodTab(page: import('@playwright/test').Page) {
  await page.goto('/reports');
  await page.locator('#report-tab-eod').click();
  await expect(page.locator('#eod-day-x-show')).toBeVisible();
}

test('day so far: Show renders the paper, Hide removes it, no page-level horizontal scroll', async ({ page }) => {
  const assertClean = watchConsole(page);
  await openEodTab(page);
  const body = page.locator('#eod-day-x-body');
  await expect(body).toBeEmpty();

  await page.locator('#eod-day-x-show').click();
  const pre = body.locator('pre.paper-preview');
  await expect(pre).toBeVisible();
  await expect(pre).toHaveAttribute('dir', 'ltr');
  await expect(pre).toContainText('X REPORT - DAY SO FAR');
  await expect(pre).toContainText('Not a day close. Nothing is closed.');
  await expect(pre).not.toContainText('END OF DAY');

  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
    await page.setViewportSize(vp);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow, `page-level horizontal scroll at ${vp.width}px`).toBe(false);
    const box = await page.locator('#eod-day-x-print').boundingBox();
    expect(box!.height).toBeGreaterThanOrEqual(36);
  }
  await page.screenshot({ path: 'test-results/eod-day-x-360.png', fullPage: true });

  await body.getByRole('button', { name: 'Hide day so far' }).click();
  await expect(body).toBeEmpty();
  await expect(page.locator('#eod-day-x-show')).toBeFocused();
  assertClean();
});

test('day so far: a failed print shows inline and keeps the preview', async ({ page }) => {
  await openEodTab(page);
  await page.locator('#eod-day-x-show').click();
  await expect(page.locator('#eod-day-x-body pre.paper-preview')).toBeVisible();
  await page.route('**/api/reports/eod/x-report/print', (route) =>
    route.fulfill({
      status: 502,
      contentType: 'text/html; charset=utf-8',
      body: "<div class='error' role='alert'>Print failed. Check the receipt printer in Settings → Receipt printer, then tap Print day so far again.</div>",
    }),
  );
  await page.locator('#eod-day-x-print').click();
  await expect(page.locator('#eod-day-x-print-msg')).toContainText('Print failed');
  await expect(page.locator('#eod-day-x-body pre.paper-preview')).toBeVisible();
});
