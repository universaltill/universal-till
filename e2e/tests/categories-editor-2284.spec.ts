import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2284: the category dialog's three pickers — colour swatch grid,
// customization-group checkboxes and kitchen-station checkboxes — on top
// of the ut-docs#2010 record dialog. What the Go tests can't see is the
// browser half: record-dialog.js's generic prefill only handles ONE form
// control per data-field-*, so the page's own script paints the pressed
// colour tile and ticks the checkbox sets inside the record-dialog:open
// hook. Two things must hold in a real browser:
//   (a) tapping a row shows the row's saved colour pressed and its saved
//       stations ticked (round-trip through Save);
//   (b) that prefill happens BEFORE the discard-guard snapshot, so opening
//       a row and pressing Escape straight away closes silently — no
//       "discard changes?" confirm for a form nobody touched.

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const GRID = '#category-color-grid';

function row(page: Page, name: string) {
  return page.locator('#categories-table .category-row', { hasText: name }).first();
}

test.describe('category editor pickers (ut-docs#2284)', () => {
  test('colour and station round-trip; prefilled open is a clean open', async ({ page }) => {
    const assertClean = watchConsole(page);
    // A station to route to, created through the app's own endpoint so the
    // dialog has a real checkbox to tick (the demo seed ships none).
    const stationName = `E2E Grill ${Date.now()}`;
    const created = await page.request.post('/api/kitchen-stations', {
      form: { name: stationName, destination_type: 'printer', printer_address: '127.0.0.1:9100' },
      maxRedirects: 0,
    });
    expect([200, 303]).toContain(created.status());

    await page.goto('/categories');
    await page.locator('#categories-new').click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();

    // The colour grid: "no colour" pressed by default, hidden input empty.
    const noneTile = page.locator(`${GRID} .item-color-tile[data-color=""]`);
    await expect(noneTile).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('#category-color')).toHaveValue('');

    const name = `Colour Cat ${Date.now()}`;
    await page.locator(NAME).fill(name);
    const tealTile = page.locator(`${GRID} .item-color-tile[data-color="#0f766e"]`);
    await tealTile.click();
    await expect(tealTile).toHaveAttribute('aria-pressed', 'true');
    await expect(noneTile).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('#category-color')).toHaveValue('#0f766e');

    // Tick the station created above by its label text.
    const stationLabel = page.locator(`${DIALOG} label`, { hasText: stationName });
    await expect(stationLabel).toHaveCount(1);
    await stationLabel.locator('input[name="station_id"]').check();

    await Promise.all([
      page.waitForURL(/\/categories$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);

    // (a) The row carries the saved colour and station; opening it shows
    // the teal tile pressed and the station ticked.
    const r = row(page, name);
    await expect(r).toHaveCount(1);
    await expect(r).toHaveAttribute('data-field-color', '#0f766e');
    await expect(r).toHaveAttribute('data-stations', /.+/);
    await expect(r.locator('.category-swatch')).toHaveCount(1);

    // (b) Opening a row must never prompt: fail loudly if any confirm()
    // appears from here on.
    page.on('dialog', async (d) => {
      await d.dismiss();
      throw new Error(`unexpected ${d.type()} dialog: ${d.message()}`);
    });
    await r.click();
    await expect(dlg).toBeVisible();
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await expect(page.locator(NAME)).toHaveValue(name);
    await expect(page.locator(`${GRID} .item-color-tile[data-color="#0f766e"]`)).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator(`${GRID} .item-color-tile[data-color=""]`)).toHaveAttribute('aria-pressed', 'false');
    await expect(stationLabel.locator('input[name="station_id"]')).toBeChecked();

    // Escape on the untouched, prefilled form closes silently.
    await page.locator(NAME).press('Escape');
    await expect(dlg).toBeHidden();

    // Create mode after an edit starts clean again: no colour, nothing ticked.
    await page.locator('#categories-new').click();
    await expect(dlg).toHaveAttribute('data-record-mode', 'create');
    await expect(page.locator(`${GRID} .item-color-tile[data-color=""]`)).toHaveAttribute('aria-pressed', 'true');
    await expect(stationLabel.locator('input[name="station_id"]')).not.toBeChecked();
    await page.locator(NAME).press('Escape');
    await expect(dlg).toBeHidden();

    assertClean();
  });
});
