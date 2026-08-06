import { test, expect } from '../support/fixtures';

// The 🐞 bug-report panel (ut-docs#346): non-modal capture panel opened
// from the nav on every staff page. Mirrors the e2e/ suite's coverage so a
// regression in either till setup is caught (a prior cycle shipped one by
// running only one of the two Playwright suites).

test('🐞 nav button opens the non-modal panel', async ({ page }) => {
  await page.goto('/');

  const toggle = page.getByTestId('bugreport-toggle');
  await expect(toggle).toBeVisible();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeHidden();

  await toggle.click();
  await expect(panel).toBeVisible();

  // Non-modal: no backdrop element exists, and the page underneath still
  // takes keystrokes — type into the sale screen's barcode input while the
  // panel stays open.
  const barcode = page.getByLabel('Barcode');
  await barcode.click();
  await barcode.fill('PANEL-OPEN-TYPING');
  await expect(barcode).toHaveValue('PANEL-OPEN-TYPING');
  await expect(panel).toBeVisible();

  // Dismissible: the ✕ collapses it.
  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();
});

test('a typed report sends inline without a page navigation', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  await page.locator('#ir-note').fill('e2e(:8080): typed report via the panel');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/issue-reports')),
    page.locator('#ir-save-btn').click(),
  ]);

  await expect(page.locator('#ir-status')).toContainText('Saved');
  await expect(page).toHaveURL(/\/$/); // never navigated away
});

// Same regression lock as e2e/tests/bugreport-panel.spec.ts, on the second
// till setup: the capture form's one-press path (note field + Save) must be
// inside the panel's visible box the moment it opens, not below its own
// inner scroll line (independent review, 2026-08-06).
test('the note field and Save are visible the moment the panel opens', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  const panel = await page.getByTestId('bugreport-panel').boundingBox();
  const note = await page.locator('#ir-note').boundingBox();
  const save = await page.locator('#ir-save-btn').boundingBox();
  for (const b of [note!, save!]) {
    expect(b.y).toBeGreaterThanOrEqual(panel!.y - 1);
    expect(b.y + b.height).toBeLessThanOrEqual(panel!.y + panel!.height + 1);
  }
});

test('/report-issue still 200s with the panel already open', async ({ page }) => {
  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  await expect(page.locator('#ir-note')).toHaveCount(1); // no duplicated capture UI
});
