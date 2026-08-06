import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The 🐞 bug-report panel (ut-docs#346): the capture UI moved out of the
// standalone /report-issue page into a floating, NON-modal panel opened
// from the nav. The acceptance criterion that must be driven for real, not
// checked by eye: with the panel open, the page underneath stays fully
// clickable, typeable and scrollable — no backdrop, no focus trap.

test('🐞 opens the panel; closed by default; ✕ closes it again', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  const toggle = page.getByTestId('bugreport-toggle');
  await expect(toggle).toBeVisible(); // auth off ⇒ manager-equivalent
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeHidden(); // no page pays for the panel until used

  await toggle.click();
  await expect(panel).toBeVisible();
  // Permanently-visible expectation note: recording ends on navigation.
  await expect(panel.locator('.bugreport-nav-note')).toBeVisible();

  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();

  // The toggle also collapses it (second click).
  await toggle.click();
  await expect(panel).toBeVisible();
  await toggle.click();
  await expect(panel).toBeHidden();
  assertClean();
});

test('non-modal: a full sale completes underneath the open panel', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();

  // Typeable underneath: the barcode input takes keystrokes with the panel
  // open (a seeded demo barcode, same as sale.spec.ts).
  const barcode = page.locator('.scan-row input[name=code]');
  await barcode.click();
  await barcode.fill('5000000000012');
  await expect(barcode).toHaveValue('5000000000012');

  // Clickable underneath: the scan submits, the basket updates…
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.20');

  // …and the tender buttons are neither covered nor disabled: cash pays
  // and the receipt view appears, all while the panel stays open.
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  assertClean();
});

test('the open panel never overlaps the basket column or the tender panel', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#basket')).toBeVisible(); // htmx load done
  await page.getByTestId('bugreport-toggle').click();

  const panel = await page.getByTestId('bugreport-panel').boundingBox();
  const basket = await page.locator('.pos-container > .basket').boundingBox();
  const tender = await page.locator('.pos-container > .tender').boundingBox();
  expect(panel).not.toBeNull();
  expect(basket).not.toBeNull();
  expect(tender).not.toBeNull();
  // LTR: basket is the start column, panel is anchored at the inline end.
  expect(panel!.x).toBeGreaterThanOrEqual(basket!.x + basket!.width - 1);
  // The panel's bounded max-height keeps it above the tender area even on
  // a short viewport — pay buttons must never sit under it.
  expect(panel!.y + panel!.height).toBeLessThanOrEqual(tender!.y + 1);
});

// Regression lock (independent review, 2026-08-06). Clearing both the basket
// column and the tender panel leaves the panel only ~13rem of vertical band
// on a till-sized screen, and the first cut spent that band on two paragraphs
// of prose: opening 🐞 showed explanatory text and NO input at all, with the
// note field and Save button below the panel's own inner scroll line. The
// one-press path the card is about has to be visible on open, at every size a
// till actually runs at — bounding boxes, not eyeballs.
for (const [w, h] of [[1280, 720], [1024, 600], [1280, 800]] as const) {
  test(`the note field and Save are visible on open at ${w}x${h}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: h });
    await page.goto('/');
    await expect(page.locator('#basket')).toBeVisible();
    await page.getByTestId('bugreport-toggle').click();

    const panel = await page.getByTestId('bugreport-panel').boundingBox();
    const note = await page.locator('#ir-note').boundingBox();
    const save = await page.locator('#ir-save-btn').boundingBox();
    for (const b of [panel, note, save]) expect(b).not.toBeNull();
    // Inside the panel's own visible box — no inner scrolling required.
    for (const b of [note!, save!]) {
      expect(b.y).toBeGreaterThanOrEqual(panel!.y - 1);
      expect(b.y + b.height).toBeLessThanOrEqual(panel!.y + panel!.height + 1);
    }
    // …and still clear of the tender, so this never trades one bug for the other.
    const tender = await page.locator('.pos-container > .tender').boundingBox();
    expect(panel!.y + panel!.height).toBeLessThanOrEqual(tender!.y + 1);
  });
}

test('scrollable underneath: a long page still scrolls with the panel open', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');
  await page.getByTestId('bugreport-toggle').click();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();

  // A real wheel gesture over the page body (not over the panel).
  const before = await page.evaluate(() => window.scrollY);
  await page.mouse.move(200, 400);
  await page.mouse.wheel(0, 600);
  await expect
    .poll(() => page.evaluate(() => window.scrollY))
    .toBeGreaterThan(before);
  assertClean();
});

test('a typed report sends inline — success reported without navigating', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  await page.locator('#ir-note').fill('e2e: typed-only report through the panel');
  await page.locator('#ir-save-btn').click();

  // Outcome lands inline in the panel's status line…
  await expect(page.locator('#ir-status')).toContainText('Saved');
  // …and the page never navigated.
  await expect(page).toHaveURL('http://127.0.0.1:8091/');
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  assertClean();
});

test('/report-issue still works and lands with the panel already open', async ({ page }) => {
  const assertClean = watchConsole(page);
  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  // Open server-side (class stamped in the markup), not via client JS.
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  await expect(page.locator('#ir-note')).toBeVisible();
  // The page body itself no longer duplicates the capture UI: exactly one
  // note textarea on the whole page (the panel's).
  await expect(page.locator('#ir-note')).toHaveCount(1);
  assertClean();
});
