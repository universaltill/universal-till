import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2568: a Till Designer category row's "Change picture or icon" link
// opens that category's existing dialog on /categories (the image / icon
// picker lives only there). The link is plain read-only navigation, so it
// stays usable while the main till is unreachable (it sits outside the
// data-main-till-lock fieldsets); the deep-link param is cleared on open so a
// reload never re-opens the dialog; an unknown id is a silent no-op.

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';

async function flipChip(page: Page, state: string) {
  await page.evaluate((s) => {
    const mount = document.getElementById('sb-main-till-mount')!;
    mount.innerHTML = `<a class="sb-item sb-link" data-link-state="${s}">x</a>`;
    mount.dispatchEvent(new CustomEvent('htmx:afterSwap', { bubbles: true }));
  }, state);
}

async function expandFirstRow(page: Page) {
  await page.goto('/designer');
  const row = page.locator('.designer-cat').first();
  await expect(row).toBeVisible();
  const name = (await row.locator('.designer-cat-name').innerText()).trim();
  await row.locator('.designer-cat-open').click();
  return { row, name };
}

test.describe('Designer category row links to the picture dialog (ut-docs#2568)', () => {
  test('the link opens that category dialog, clears the param, and a reload does not reopen it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { row, name } = await expandFirstRow(page);
    const link = row.locator('[data-testid^="designer-cat-image-"]');
    await expect(link).toBeVisible();
    const box = await link.boundingBox();
    expect(box!.height, 'touch target >= 44px').toBeGreaterThanOrEqual(43.5);
    await link.click();

    await expect(page).toHaveURL(/\/categories(\?(?!.*\bedit=).*)?$/);
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue(name);
    await expect(page.locator(`${DIALOG} #category-image-choose-btn`)).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await page.reload();
    await expect(page.locator('#categories-table')).toBeVisible();
    await expect(page.locator(DIALOG)).toBeHidden();
    assertClean();
  });

  test('an unknown id opens nothing and leaves the page clean', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories?edit=does-not-exist');
    await expect(page.locator('#categories-table')).toBeVisible();
    await expect(page.locator(DIALOG)).toBeHidden();
    expect(page.url()).not.toContain('edit=');
    assertClean();
  });

  test('the link stays usable while the main till is unreachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { row } = await expandFirstRow(page);
    await flipChip(page, 'unreachable');
    await expect(row.locator('[data-testid^="designer-cat-image-"]')).toBeEnabled();
    await expect(row.locator('.designer-cat-form input[name="name"]')).toBeDisabled();
    await row.locator('[data-testid^="designer-cat-image-"]').click();
    await expect(page.locator(DIALOG)).toBeVisible();
    assertClean();
  });
});
