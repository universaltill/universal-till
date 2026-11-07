import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#5797: the X report (mid-shift read) on /shifts. The Go handler
// tests prove the fragment and the gate; only a browser proves the htmx
// wiring: one tap shows the figures, the button is disabled in flight, Hide
// removes the figures from the DOM (not just hides them), a 404 race and a
// dropped connection both land inline in #x-report-body, and the result
// fits a 360px phone with no page-level horizontal scroll.
//
// The default project's till is shared per worker and other specs open and
// close shifts, so every test establishes its own state (an open shift with
// one cash sale) instead of assuming one.

async function ensureOpenShift(page: Page) {
  await page.goto('/shifts');
  if ((await page.locator('#close-shift-form').count()) === 0) {
    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
  }
}

async function ringCashSale(page: Page) {
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
}

async function closeOpenShift(page: Page) {
  await page.evaluate(async () => {
    const el = document.querySelector('#close-shift-form input[name="shift_id"]') as HTMLInputElement | null;
    if (!el) return;
    await fetch('/api/shifts/close', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ shift_id: el.value, closing_cash: '0' }),
    });
  });
}

test('X report: one tap shows figures, Hide removes them from the DOM, GET carries none', async ({ page }) => {
  const assertClean = watchConsole(page);
  await ensureOpenShift(page);
  await ringCashSale(page);

  const resp = await page.goto('/shifts');
  const html = (await resp!.text()) ?? '';
  // Privacy (UX C15): the page itself carries no X figures before Show.
  expect(html).not.toContain('Figures as of');
  expect(html).not.toContain('x-report-result');

  const section = page.locator('#x-report');
  await expect(section).toBeVisible();
  await expect(section.locator('h3')).toHaveText('X report');
  const body = page.locator('#x-report-body');
  await expect(body).toBeEmpty();

  // Loading (UX C6): the button is disabled while the request is in flight
  // and a double tap sends one request.
  let posts = 0;
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => (release = r));
  await page.route('**/shifts/x-report', async (route) => {
    posts++;
    await gate;
    await route.continue();
  });
  const show = page.locator('#x-report-show');
  await show.click();
  await expect(show).toBeDisabled();
  await show.click({ force: true }).catch(() => {});
  release();
  await expect(body.locator('.x-report-result')).toBeVisible();
  expect(posts).toBe(1);
  await expect(show).toBeEnabled();
  await page.unroute('**/shifts/x-report');

  await expect(body).toContainText('Figures as of');
  await expect(body).toContainText('Expected');
  await expect(body).toContainText('Cash');
  // no Z wording in the X fragment
  await expect(body).not.toContainText('End of day');

  // Hide: one tap, figures gone from the DOM, focus back on Show.
  await body.getByRole('button', { name: 'Hide X report' }).click();
  await expect(body).toBeEmpty();
  await expect(page.locator('.x-report-result')).toHaveCount(0);
  await expect(show).toBeFocused();
  assertClean();
});

test('X report: touch targets and no page scroll at 1024x600 and 360px', async ({ page }) => {
  await ensureOpenShift(page);
  await ringCashSale(page); // the tender table only renders with a payment
  for (const vp of [
    { width: 1024, height: 600 },
    { width: 360, height: 740 },
  ]) {
    await page.setViewportSize(vp);
    await page.goto('/shifts');
    await page.locator('#x-report-show').click();
    const result = page.locator('#x-report-body .x-report-result');
    await expect(result).toBeVisible();
    const where = `${vp.width}x${vp.height}`;
    for (const sel of ['#x-report-show', '#x-report-print', '#x-report-body .x-report-bar button[data-action]']) {
      const box = await page.locator(sel).boundingBox();
      expect(box, `${sel} at ${where}`).not.toBeNull();
      expect(box!.height, `${sel} height at ${where}`).toBeGreaterThanOrEqual(44);
    }
    const scroll = await page.evaluate(() => ({
      sw: document.documentElement.scrollWidth,
      cw: document.documentElement.clientWidth,
    }));
    expect(scroll.sw, `page scrolls horizontally at ${where}`).toBeLessThanOrEqual(scroll.cw);
    // Phone cards (UX C3): the totals table stays a real table, so its row
    // names stay in the first cell; table-cards.js must not restack it.
    const totals = result.locator('table.x-report-num[data-cards="off"]').first();
    await expect(totals.locator('tbody tr').first().locator('td').first()).toHaveText('Sales');
    await expect(totals).not.toHaveClass(/t-cards/);
    if (vp.width === 360) {
      // Tenders (UX C3) stack into cards titled by the method name, with all
      // four figures on the card: no More toggle hiding Tips.
      const tenders = result.locator('table.x-report-tenders');
      await expect(tenders.locator('tbody tr').first().locator('td').first()).toHaveClass(/t-cards-title/);
      await expect(tenders.locator('.t-cards-toggle:visible')).toHaveCount(0);
      await expect(tenders.locator('tbody tr').first().locator('td').nth(4)).toBeVisible();
    }
  }
});

test('X report: a shift closed elsewhere shows the inline 404 text', async ({ page }) => {
  await ensureOpenShift(page);
  await page.goto('/shifts');
  await expect(page.locator('#x-report-show')).toBeVisible();
  await closeOpenShift(page); // the page still shows the stale Show button
  await page.locator('#x-report-show').click();
  const err = page.locator('#x-report-body [role="alert"]');
  await expect(err).toContainText('This register has no open shift');
  await expect(page.locator('#x-report-body .x-report-result')).toHaveCount(0);

  // with no open shift the section is absent altogether
  await page.goto('/shifts');
  await expect(page.locator('#x-report')).toHaveCount(0);
});

test('X report: a dropped connection shows the server error inline, not a dead button', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);
  await ensureOpenShift(page);
  await page.goto('/shifts');
  await page.route('**/shifts/x-report', (route) => route.abort());
  await page.locator('#x-report-show').click();
  const err = page.locator('#x-report-body [role="alert"]');
  await expect(err).toContainText('Something went wrong');
  await expect(page.locator('#x-report-show')).toBeEnabled();
  assertClean();
});

test('X report: Reports -> End of day links to the X section', async ({ page }) => {
  await ensureOpenShift(page);
  await page.goto('/reports');
  await page.locator('#report-tab-eod').click();
  const link = page.locator('a.x-report-link[href="/shifts#x-report"]');
  await expect(link).toBeVisible();
  const box = await link.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  await link.click();
  await expect(page).toHaveURL(/\/shifts#x-report$/);
  await expect(page.locator('#x-report')).toBeInViewport();
});

test('X report: a print failure shows inline and keeps the figures (ut-docs#5798)', async ({ page }) => {
  await ensureOpenShift(page);
  await page.goto('/shifts');
  await page.locator('#x-report-show').click();
  await expect(page.locator('#x-report-body .x-report-result')).toBeVisible();

  await page.route('**/x-report/print', (route) =>
    route.fulfill({ status: 502, contentType: 'text/html', body: "<div class='error' role='alert'>Print failed</div>" }));
  await page.locator('#x-report-print').click();
  await expect(page.locator('#x-report-print-msg')).toContainText('Print failed');
  await expect(page.locator('#x-report-body .x-report-result')).toBeVisible();

  await page.unroute('**/x-report/print');
  await page.route('**/x-report/print', (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: "<span class='x-report-print-ok' role='status'>\u2713 X report sent to the printer</span>" }));
  await page.locator('#x-report-print').click();
  await expect(page.locator('#x-report-print-msg')).toContainText('X report sent to the printer');
  await expect(page.locator('#x-report-body .x-report-result')).toBeVisible();
});

// Tester (ut-docs#5798): a 502 HTML body is force-swapped by app.js's global
// beforeSwap, so the test above passes without shifts.html's XR_TARGETS
// entry; only a dropped connection (htmx:sendError) proves that entry.
test('X report: a dropped connection on Print shows inline and keeps the figures (ut-docs#5798)', async ({ page }) => {
  const assertClean = watchConsole(page, /^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/);
  await ensureOpenShift(page);
  await page.goto('/shifts');
  await page.locator('#x-report-show').click();
  await expect(page.locator('#x-report-body .x-report-result')).toBeVisible();
  await page.route('**/x-report/print', (route) => route.abort());
  await page.locator('#x-report-print').click();
  await expect(page.locator('#x-report-print-msg [role="alert"]')).toContainText('Something went wrong');
  await expect(page.locator('#x-report-print')).toBeEnabled();
  await expect(page.locator('#x-report-body .x-report-result')).toBeVisible();
  assertClean();
});
