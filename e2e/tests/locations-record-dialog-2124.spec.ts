import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2124: /locations adopts the ut-docs#2010 list/edit dialog
// standard, mirroring categories-record-dialog-2010.spec.ts's proof for
// the reference implementation. This spec does NOT re-prove what that one
// already proves about the SHARED partial/JS (record_dialog.html,
// list_header.html, record-dialog.js are the same code, unchanged here) —
// it proves Locations' own template wiring (data-record-* attributes,
// the two record_dialog_fields/record_dialog_destructive slots) actually
// drives that shared mechanism correctly end to end, through a real
// browser, which the Go-level httptest coverage in
// locations_page_test.go cannot see (a typo in a data-record-* attribute
// name is invisible to a server-rendered-HTML-string assertion but breaks
// the dialog outright in a real browser).

const DIALOG = '#location-dialog';
const NAME = '#location-form input[name="name"]';

async function createLocation(page: Page, name: string) {
  await page.locator('#locations-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator(NAME).fill(name);
  await Promise.all([
    page.waitForURL(/\/locations$/),
    page.locator(`${DIALOG} .record-dialog-save`).click(),
  ]);
  await expect(page.locator('#locations-table .location-row', { hasText: name })).toHaveCount(1);
}

function row(page: Page, name: string) {
  return page.locator('#locations-table .location-row', { hasText: name }).first();
}

test.describe('locations list + record dialog (ut-docs#2124)', () => {
  test('New opens the dialog empty in create mode, and create works', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/locations');
    expect(await page.evaluate(() => document.activeElement === document.body)).toBe(true);
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeHidden();

    const newBtn = page.locator('#locations-new');
    await expect(newBtn).toHaveAttribute('aria-label', /.+/);
    await expect(newBtn).toHaveAttribute('title', /.+/);
    await expect(newBtn).toHaveText('');

    const name = 'E2E Back Room ' + Date.now();
    await createLocation(page, name);
    assertClean();
  });

  test('row tap opens edit prefilled, rename saves, deactivate/activate round-trips', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/locations');
    const name = 'E2E Storeroom ' + Date.now();
    await createLocation(page, name);

    // Tap the row (not just the pencil button) to open edit mode,
    // prefilled from the row's data-field-* attributes.
    await row(page, name).click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue(name);
    // Active by default — the "deactivate" (trash) control is the one
    // shown, not "activate".
    await expect(page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`)).toBeVisible();

    const renamed = name + ' Renamed';
    await page.locator(NAME).fill(renamed);
    await Promise.all([
      page.waitForURL(/\/locations$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, renamed)).toHaveCount(1);
    await expect(page.locator('#locations-table .location-row', { hasText: name + ' Renamed Renamed' })).toHaveCount(0);

    // Deactivate — this location has no stock/register, so the in-use
    // guard doesn't block it (locations_page_test.go's own
    // TestLocationsPage_DeactivatableOnceStockCleared covers the guard's
    // server-side logic; this proves the dialog's own delivery of it).
    // hx-confirm (locations.deactivate_confirm) pops a real browser
    // confirm() — Playwright auto-dismisses an unhandled one, which would
    // silently no-op the click, so accept it explicitly first (same
    // pattern as categories-record-dialog-2010.spec.ts's own (b3)).
    await row(page, renamed).click();
    await expect(dlg).toBeVisible();
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForURL(/\/locations$/),
      page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`).click(),
    ]);
    await expect(row(page, renamed)).toContainText(/inactive/i);

    // Activate reverses it, from the same dialog — no confirm on this path.
    await row(page, renamed).click();
    await expect(dlg).toBeVisible();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`)).toBeVisible();
    await Promise.all([
      page.waitForURL(/\/locations$/),
      page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`).click(),
    ]);
    await expect(row(page, renamed)).toContainText(/\bactive\b/);
    await expect(row(page, renamed)).not.toContainText('inactive');
    assertClean();
  });

  test('a refused mutation (empty name) stays in-dialog, does not close or redirect', async ({ page }) => {
    // The 400 IS the deliberate response shape (renderLocationsDialogError,
    // mirroring categories_page.go's own ut-docs#2020 pattern) — Chromium
    // still logs a non-2xx resource load to the console regardless of how
    // the page's own JS handles it (same exemption categories-record-
    // dialog-2010.spec.ts's own (g) test carries).
    const assertClean = watchConsole(page, /Failed to load resource:.*400/);
    await page.goto('/locations');
    await page.locator('#locations-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await page.locator(NAME).fill('   ');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    // Refused server-side (whitespace-only trims to empty) — the dialog
    // must still be open and on /locations, not redirected away.
    await expect(dlg).toBeVisible();
    await expect(page).toHaveURL(/\/locations$/);
    await expect(page.locator(`${DIALOG} [data-record-dialog-msg]`)).not.toBeEmpty();
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600' },
    { width: 360, height: 740, label: 'phone 360px' },
  ]) {
    test(`at ${vp.label} the dialog is full-bleed and status/lock/exit-to-OS stay reachable`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/locations');
      const name = 'Viewport Probe ' + Date.now();
      await createLocation(page, name);
      await row(page, name).click();
      const dlg = page.locator(DIALOG);
      await expect(dlg).toBeVisible();

      const box = (await dlg.boundingBox())!;
      expect(box.x).toBe(0);
      expect(box.y).toBe(0);
      expect(Math.round(box.width)).toBe(vp.width);
      expect(Math.round(box.height)).toBe(vp.height);

      // ut-docs#1999/#2099 (coding-standards.md §10): status/lock/exit-to-OS
      // reachable without closing the dialog, at every surface admin
      // included — this is generic record_dialog.html behavior, but it's
      // still worth pinning per-page since it's the whole reason this
      // dialog is allowed to cover the nav rail at all.
      const conn = page.locator(`${DIALOG} [data-record-dialog-conn]`);
      const exitLink = page.locator(`${DIALOG} [data-record-dialog-exit]`);
      const lockBtn = page.locator(`${DIALOG} [data-record-dialog-lock] button`);
      for (const el of [conn, exitLink, lockBtn]) {
        await expect(el).toBeVisible();
        const b = (await el.boundingBox())!;
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.y).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(vp.width + 0.5);
        expect(b.y + b.height).toBeLessThanOrEqual(vp.height + 0.5);
      }

      // No horizontal overflow introduced by opening the dialog.
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);

      // A real screenshot, looked at by hand during this card's review —
      // not asserted on pixel-for-pixel (that's guard-docs-shots.sh's own
      // job for the manual's own captured topics; /locations itself is
      // never one of those, ut-docs#900), saved here so a reviewer can
      // open it directly rather than re-driving the app.
      await page.screenshot({ path: `test-results/locations-dialog-${vp.width}x${vp.height}.png` });
      assertClean();
    });
  }
});
