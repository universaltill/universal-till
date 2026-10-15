import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#3605: on an additional till the Categories record dialog's form
// sits in a fieldset[data-main-till-lock] that base.html's chip follower
// disables LIVE when the main till becomes unreachable. Disabled controls
// drop out of FormData, so record-dialog.js's discard guard (which compares
// a FormData snapshot taken at open with one taken at close) read the lock
// flip itself as an unsaved edit: Close / Escape asked "Discard your unsaved
// changes?" though nothing was typed (seen live on a real two-till setup,
// Tester). The flip is driven here through the real follower — a chip with
// data-link-state="unreachable" swapped into #sb-main-till-mount, exactly
// what the 5 s /ui/main-till-status poll does on a replica — on the
// e2e's own (main) till, whose /categories renders the same fieldsets
// enabled.

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';

async function flipChip(page: Page, state: string) {
  await page.evaluate((s) => {
    const mount = document.getElementById('sb-main-till-mount')!;
    mount.innerHTML = `<a class="sb-item sb-link" data-link-state="${s}">x</a>`;
    mount.dispatchEvent(new CustomEvent('htmx:afterSwap', { bubbles: true }));
  }, state);
}

async function openFirstRow(page: Page) {
  await page.goto('/categories');
  await page.locator('#categories-table .category-row-open').first().click();
  await expect(page.locator(DIALOG)).toBeVisible();
}

test.describe('categories dialog discard guard vs the main-till lock (ut-docs#3605)', () => {
  test('a lock flip while the dialog is open is not an unsaved edit', async ({ page }) => {
    const assertClean = watchConsole(page);
    const confirms: string[] = [];
    page.on('dialog', (d) => { confirms.push(d.message()); void d.dismiss(); });
    await openFirstRow(page);

    await flipChip(page, 'unreachable');
    await expect(page.locator(NAME)).toBeDisabled();
    await expect(page.locator(`${DIALOG} .record-dialog-save`)).toBeDisabled();

    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.locator(DIALOG)).toBeHidden();
    expect(confirms, 'nothing was typed: Close must not ask to discard').toEqual([]);

    // And back: reopened while locked, the lock lifting is not an edit either.
    await page.locator('#categories-table .category-row-open').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await flipChip(page, 'linked');
    await expect(page.locator(NAME)).toBeEnabled();
    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    expect(confirms).toEqual([]);
    assertClean();
  });

  test('a real edit made before the lock still asks first', async ({ page }) => {
    const confirms: string[] = [];
    page.on('dialog', (d) => { confirms.push(d.message()); void d.dismiss(); });
    await openFirstRow(page);
    await page.locator(NAME).fill('Edited before the lock 3605');

    await flipChip(page, 'unreachable');
    await expect(page.locator(NAME)).toBeDisabled();

    await page.locator(`${DIALOG} .record-dialog-close`).click();
    expect(confirms).toHaveLength(1);
    // Dismissed: the dialog stays, with the typed text still there.
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('Edited before the lock 3605');
    // Nothing is left changed: the lock is lifted and the dialog closed
    // without saving (accept the discard).
    page.removeAllListeners('dialog');
    page.on('dialog', (d) => void d.accept());
    await flipChip(page, 'linked');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.locator(DIALOG)).toBeHidden();
  });
});
