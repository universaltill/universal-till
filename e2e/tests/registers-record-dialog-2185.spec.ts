import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2185: /registers adopts the ut-docs#2010 list/edit dialog
// standard, mirroring locations-record-dialog-2124.spec.ts's proof for
// its near-twin (ut-docs#2124). Same rationale as that file's own header
// comment: this proves Registers' own template wiring (the location_id
// <select> in particular — the one structural difference from Locations)
// drives the shared record_dialog.html/list_header.html/record-dialog.js
// mechanism correctly through a real browser.

const DIALOG = '#register-dialog';
const NAME = '#register-form input[name="name"]';
const LOCATION = '#register-form select[name="location_id"]';

async function createRegister(page: Page, name: string) {
  await page.locator('#registers-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator(NAME).fill(name);
  await Promise.all([
    page.waitForURL(/\/registers$/),
    page.locator(`${DIALOG} .record-dialog-save`).click(),
  ]);
  await expect(page.locator('#registers-table .register-row', { hasText: name })).toHaveCount(1);
}

function row(page: Page, name: string) {
  return page.locator('#registers-table .register-row', { hasText: name }).first();
}

test.describe('registers list + record dialog (ut-docs#2185)', () => {
  test('New opens the dialog empty in create mode, and create with a location works', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/registers');
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeHidden();

    const newBtn = page.locator('#registers-new');
    await expect(newBtn).toHaveAttribute('aria-label', /.+/);
    await expect(newBtn).toHaveAttribute('title', /.+/);
    await expect(newBtn).toHaveText('');
    await newBtn.click();
    await expect(dlg).toBeVisible();
    // Blank in create mode — no location preselected.
    await expect(page.locator(LOCATION)).toHaveValue('');

    const name = 'E2E Front Counter ' + Date.now();
    await page.locator(NAME).fill(name);
    // Pick whichever real location option comes first (seeded data varies
    // by fixture; this only needs to prove the select actually submits).
    const options = await page.locator(`${LOCATION} option[value]:not([value=""])`).all();
    if (options.length > 0) {
      const val = await options[0].getAttribute('value');
      await page.locator(LOCATION).selectOption(val!);
    }
    await Promise.all([
      page.waitForURL(/\/registers$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, name)).toHaveCount(1);
    assertClean();
  });

  test('row tap opens edit prefilled (name AND location), rename+relocate saves, deactivate/activate round-trips', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/registers');
    const name = 'E2E Back Counter ' + Date.now();
    await createRegister(page, name);

    await row(page, name).click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue(name);
    await expect(page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`)).toBeVisible();

    const renamed = name + ' Renamed';
    await page.locator(NAME).fill(renamed);
    const options = await page.locator(`${LOCATION} option[value]:not([value=""])`).all();
    if (options.length > 0) {
      const val = await options[0].getAttribute('value');
      await page.locator(LOCATION).selectOption(val!);
      await Promise.all([
        page.waitForURL(/\/registers$/),
        page.locator(`${DIALOG} .record-dialog-save`).click(),
      ]);
      // Reopen: the location just picked is still selected — proves the
      // row's data-field-location_id prefill round-trips through a real
      // save, not just the name field.
      await row(page, renamed).click();
      await expect(dlg).toBeVisible();
      await expect(page.locator(LOCATION)).toHaveValue(val!);
    } else {
      await Promise.all([
        page.waitForURL(/\/registers$/),
        page.locator(`${DIALOG} .record-dialog-save`).click(),
      ]);
      await row(page, renamed).click();
      await expect(dlg).toBeVisible();
    }

    // Deactivate — behind the real hx-confirm browser dialog (Playwright
    // auto-dismisses an unhandled one, which would silently no-op the
    // click, so accept it explicitly).
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForURL(/\/registers$/),
      page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`).click(),
    ]);
    await expect(row(page, renamed)).toContainText(/inactive/i);

    // Activate reverses it, from the same dialog — no confirm on this path.
    await row(page, renamed).click();
    await expect(dlg).toBeVisible();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`)).toBeVisible();
    await Promise.all([
      page.waitForURL(/\/registers$/),
      page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`).click(),
    ]);
    await expect(row(page, renamed)).toContainText(/\bactive\b/);
    await expect(row(page, renamed)).not.toContainText('inactive');
    assertClean();
  });

  test('a refused mutation (empty name) stays in-dialog, does not close or redirect', async ({ page }) => {
    const assertClean = watchConsole(page, /Failed to load resource:.*400/);
    await page.goto('/registers');
    await page.locator('#registers-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await page.locator(NAME).fill('   ');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(dlg).toBeVisible();
    await expect(page).toHaveURL(/\/registers$/);
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
      await page.goto('/registers');
      const name = 'Viewport Probe ' + Date.now();
      await createRegister(page, name);
      await row(page, name).click();
      const dlg = page.locator(DIALOG);
      await expect(dlg).toBeVisible();

      const box = (await dlg.boundingBox())!;
      expect(box.x).toBe(0);
      expect(box.y).toBe(0);
      expect(Math.round(box.width)).toBe(vp.width);
      expect(Math.round(box.height)).toBe(vp.height);

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

      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);

      await page.screenshot({ path: `test-results/registers-dialog-${vp.width}x${vp.height}.png` });
      assertClean();
    });
  }
});
