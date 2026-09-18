import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2187: /country-settings adopts the ut-docs#2010 list/edit dialog
// standard, mirroring locations-record-dialog-2124.spec.ts's shape (the
// closest structural precedent, per that card's own comment). This spec
// does NOT re-prove what categories-record-dialog-2010.spec.ts already
// proves about the SHARED partial/JS (record_dialog.html, list_header.html,
// record-dialog.js are the same code, unchanged here) — it proves Country
// settings' own template wiring (data-record-* attributes, the two
// record_dialog_fields/record_dialog_destructive slots, and this screen's
// own extra record-dialog:open listener that locks the `code` field once
// editing) actually drives that shared mechanism correctly end to end,
// through a real browser, which the Go-level httptest coverage in
// country_settings_page_test.go cannot see.

const DIALOG = '#country-dialog';
const CODE = '#country-form input[name="code"]';
const CURRENCY = '#country-form input[name="currency"]';
const TAX = '#country-form input[name="tax_rate_pct"]';
const RETENTION = '#country-form input[name="archive_min_days"]';

function row(page: Page, code: string) {
  return page.locator('#country-settings-table .country-row', { hasText: code }).first();
}

// A fresh, valid, custom (never-builtin) country code every run — 8 chars
// max (data.CountrySettingsRepo's own limit), letters/digits only.
function newCode(): string {
  return ('Z' + Date.now().toString(36).slice(-6)).toUpperCase().slice(0, 8);
}

test.describe('country settings list + record dialog (ut-docs#2187)', () => {
  test('New opens the dialog with no country prefilled, and create works', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings?all=1');
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeHidden();

    const newBtn = page.locator('#country-settings-new');
    await expect(newBtn).toHaveAttribute('aria-label', /.+/);
    await expect(newBtn).toHaveAttribute('title', /.+/);
    await expect(newBtn).toHaveText('');

    await newBtn.click();
    await expect(dlg).toBeVisible();
    // Nothing prefilled — this is a genuinely new record, and the code field
    // is free-text and editable (create mode), unlike edit mode below.
    await expect(page.locator(CODE)).toHaveValue('');
    await expect(page.locator(CODE)).not.toHaveAttribute('readonly', '');
    await expect(page.locator(CURRENCY)).toHaveValue('');

    const code = newCode();
    await page.locator(CODE).fill(code);
    await page.locator(CURRENCY).fill('GBP');
    await Promise.all([
      page.waitForURL(/\/country-settings\?all=1$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, code)).toHaveCount(1);
    await expect(row(page, code)).toContainText('GBP');
    // A freshly-created country is never builtin, so its badge shows.
    await expect(row(page, code)).toContainText(/added here/i);
    assertClean();
  });

  test('row tap opens edit prefilled, the code field is locked, and rename-adjacent fields save', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings?all=1');
    await page.locator('#country-settings-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    const code = newCode();
    await page.locator(CODE).fill(code);
    await page.locator(CURRENCY).fill('EUR');
    await Promise.all([
      page.waitForURL(/\/country-settings\?all=1$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);

    // Tap the row (not just the pencil button) to open edit mode, prefilled
    // from the row's data-field-* attributes.
    await row(page, code).click();
    await expect(dlg).toBeVisible();
    await expect(page.locator(CODE)).toHaveValue(code);
    // ut-docs#2187's own design decision: the code identifies which row a
    // save applies to, so it must not be editable once opened for edit.
    await expect(page.locator(CODE)).toHaveAttribute('readonly', '');
    await expect(page.locator(CURRENCY)).toHaveValue('EUR');
    // A custom (never-builtin) country's destructive control is "Delete",
    // not "Restore defaults".
    await expect(page.locator(`${DIALOG} form[data-record-when="is_builtin=0"] button[type="submit"]`)).toBeVisible();
    await expect(page.locator(`${DIALOG} form[data-record-when="is_builtin=1"] button[type="submit"]`)).toBeHidden();

    await page.locator(CURRENCY).fill('USD');
    await page.locator(TAX).fill('12.5');
    await Promise.all([
      page.waitForURL(/\/country-settings\?all=1$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, code)).toContainText('USD');
    // formatBPAsPercent only trims a trailing ".00" — a genuinely fractional
    // rate keeps both decimal places.
    await expect(row(page, code)).toContainText('12.50%');
    assertClean();
  });

  test('closing with unsaved changes asks first (discard guard)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings?all=1');
    await page.locator('#country-settings-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await page.locator(CODE).fill(newCode());

    let confirmed = false;
    page.once('dialog', (d) => {
      confirmed = true;
      d.dismiss();
    });
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    expect(confirmed).toBe(true);
    // Dismissed the confirm — the dialog must still be open, nothing lost.
    await expect(dlg).toBeVisible();

    page.once('dialog', (d) => d.accept());
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    assertClean();
  });

  test('a refused save (whitespace-only code) stays in-dialog, does not close or redirect', async ({ page }) => {
    // The 400 IS the deliberate response shape (renderCountrySettingsDialogError,
    // mirroring categories_page.go's own ut-docs#2020 pattern) — Chromium
    // still logs a non-2xx resource load to the console regardless of how
    // the page's own JS handles it (same exemption categories-record-
    // dialog-2010.spec.ts's own (g) test carries).
    //
    // Whitespace, not an empty string: the code field is HTML5 `required`,
    // which blocks the request from ever being SENT for a truly empty
    // value (the same reason a below-floor archive_min_days can't be used
    // here either — its `min` attribute mirrors the server floor exactly,
    // so a real browser refuses to submit it at all). "   " satisfies
    // `required` (non-empty) client-side but trims to empty server-side —
    // the same trick locations-record-dialog-2124.spec.ts's own refusal
    // case uses for its `name` field.
    const assertClean = watchConsole(page, /Failed to load resource:.*400/);
    await page.goto('/country-settings?all=1');
    await page.locator('#country-settings-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await page.locator(CODE).fill('   ');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(dlg).toBeVisible();
    await expect(page).toHaveURL(/\/country-settings\?all=1$/);
    await expect(page.locator(`${DIALOG} [data-record-dialog-msg]`)).not.toBeEmpty();
    assertClean();
  });

  test('a builtin country offers "Restore defaults" (never "Delete"), and it round-trips', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/country-settings?all=1');
    // GB ships as a builtin country (data.BuiltinCountryDefaults) — every
    // seeded till has it, so this doesn't depend on the create test above.
    await row(page, 'GB').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await expect(page.locator(CODE)).toHaveValue('GB');
    await expect(page.locator(CODE)).toHaveAttribute('readonly', '');
    await expect(page.locator(`${DIALOG} form[data-record-when="is_builtin=1"] button[type="submit"]`)).toBeVisible();
    await expect(page.locator(`${DIALOG} form[data-record-when="is_builtin=0"] button[type="submit"]`)).toBeHidden();

    // Edit its tax rate first, so the restore is genuinely observable.
    await page.locator(TAX).fill('1');
    await Promise.all([
      page.waitForURL(/\/country-settings\?all=1$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, 'GB')).toContainText('1%');

    await row(page, 'GB').click();
    await expect(dlg).toBeVisible();
    // hx-confirm pops a real browser confirm() — accept it explicitly (same
    // pattern as locations-record-dialog-2124.spec.ts's own deactivate case).
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForURL(/\/country-settings\?all=1$/),
      page.locator(`${DIALOG} form[data-record-when="is_builtin=1"] button[type="submit"]`).click(),
    ]);
    // Restored to its shipped default (20%), not removed — GB is still
    // listed, and still builtin.
    await expect(row(page, 'GB')).toHaveCount(1);
    await expect(row(page, 'GB')).toContainText('20%');
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600' },
    { width: 360, height: 740, label: 'phone 360px' },
  ]) {
    test(`at ${vp.label} the dialog is full-bleed and status/lock/exit-to-OS stay reachable`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/country-settings?all=1');
      await row(page, 'GB').click();
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
      await page.screenshot({ path: `test-results/country-settings-dialog-${vp.width}x${vp.height}.png` });
      assertClean();
    });
  }
});
