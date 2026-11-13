import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, openNewItemForm, closeItemForm, setOrderTypePromptMode, answerTillConfirm, noNativeDialogs } from './helpers';

// ut-docs#5553 (follow-up to #1103): the single-select colour pickers and the
// basket order type are WAI-ARIA radio groups with a roving tabindex and
// selecting arrow keys (radiogroup.js, loaded globally from base.html), and
// the global text-input focus ring is opaque (>= 3:1, WCAG 1.4.11). The
// forced-colors half lives in picker-forced-colors-5553.spec.ts; the
// photo-cutout background group is covered in photo-cutout-3127.spec.ts
// (it needs the fake rembg till). Icon grids are focus-only listboxes
// (icon-grid-listbox-5601.spec.ts).

const ITEM_GRID = '#item-color-grid';
const CAT_GRID = '#category-color-grid';

function row(page: Page, name: string) {
  return page.locator('#categories-table .category-row', { hasText: name }).first();
}

test.describe('picker radiogroups (ut-docs#5553)', () => {
  test('catalog colour grid: one Tab stop, arrows select, wraps, update the hidden field', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await openNewItemForm(page);
    const grid = page.locator(ITEM_GRID);
    await expect(grid).toHaveAttribute('role', 'radiogroup');
    await expect(grid).toHaveAttribute('data-roving-radiogroup', '');
    await expect(grid.locator('[aria-pressed]')).toHaveCount(0);
    await expect(grid.locator('[role="radio"]')).not.toHaveCount(0);
    const tiles = grid.locator('.item-color-tile');
    const n = await tiles.count();
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    const none = grid.locator('.item-color-tile[data-color=""]');
    await expect(none).toHaveAttribute('tabindex', '0');
    await expect(none).toHaveAttribute('aria-checked', 'true');

    // One Tab stop: Shift+Tab leaves the grid, Tab comes back onto the
    // checked tile, a further Tab leaves again.
    await none.focus();
    await page.keyboard.press('Shift+Tab');
    expect(await page.evaluate((s) => !!document.activeElement?.closest(s), ITEM_GRID)).toBe(false);
    await page.keyboard.press('Tab');
    await expect(none).toBeFocused();
    await page.keyboard.press('Tab');
    expect(await page.evaluate((s) => !!document.activeElement?.closest(s), ITEM_GRID)).toBe(false);

    const first = tiles.nth(0);
    const second = tiles.nth(1);
    const last = tiles.nth(n - 1);
    await first.click();
    await expect(first).toHaveAttribute('aria-checked', 'true');
    await page.keyboard.press('ArrowRight');
    await expect(second).toBeFocused();
    await expect(second).toHaveAttribute('aria-checked', 'true');
    await expect(second).toHaveAttribute('tabindex', '0');
    await expect(first).toHaveAttribute('tabindex', '-1');
    await expect(page.locator('#item-color')).toHaveValue((await second.getAttribute('data-color')) || '');
    await page.keyboard.press('End');
    await expect(last).toHaveAttribute('aria-checked', 'true');
    await expect(page.locator('#item-color')).toHaveValue('');
    await page.keyboard.press('Home');
    await expect(first).toHaveAttribute('aria-checked', 'true');
    await page.keyboard.press('ArrowLeft');
    await expect(last).toHaveAttribute('aria-checked', 'true');
    await expect(last).toBeFocused();

    // Arrow selection goes through the tile's own click path, like a tap:
    // the submitted hidden field follows it (the item form has no discard
    // guard to assert on, so the hidden input IS the observable effect).
    await page.keyboard.press('Home');
    await expect(first).toHaveAttribute('aria-checked', 'true');
    await expect(page.locator('#item-color')).toHaveValue((await first.getAttribute('data-color')) || '');
    await closeItemForm(page);
    assertClean();
  });

  test('catalog colour grid: an off-palette stored colour leaves the first tile as the Tab stop', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    const card = page.locator('.catalog-row').first();
    await expect(card).toBeVisible();
    await card.evaluate((el) => el.setAttribute('data-color', '#123456'));
    await card.click();
    const grid = page.locator(ITEM_GRID);
    await expect(grid).toBeVisible();
    await expect(grid.locator('[aria-checked="true"]')).toHaveCount(0);
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(grid.locator('.item-color-tile').first()).toHaveAttribute('tabindex', '0');
    await closeItemForm(page);
    assertClean();
  });

  test('category dialog colour grid: saved colour is the Tab stop, off-palette falls back, arrows dirty', async ({ page }) => {
    const assertClean = watchConsole(page);
    const none = noNativeDialogs(page);
    await page.goto('/categories');
    await page.locator('#categories-new').click();
    const dlg = page.locator('#category-dialog');
    await expect(dlg).toBeVisible();
    const grid = page.locator(CAT_GRID);
    await expect(grid).toHaveAttribute('role', 'radiogroup');
    await expect(grid).toHaveAttribute('data-roving-radiogroup', '');
    await expect(grid.locator('[aria-pressed]')).toHaveCount(0);

    const name = `Radio Cat ${Date.now()}`;
    await page.locator('#category-form input[name="name"]').fill(name);
    const teal = grid.locator('.item-color-tile[data-color="#0f766e"]');
    await teal.click();
    await Promise.all([
      page.waitForNavigation().catch(() => {}),
      page.locator('#category-dialog .record-dialog-save').click(),
    ]);
    await page.goto('/categories');

    const r = row(page, name);
    await r.click();
    await expect(dlg).toBeVisible();
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(teal).toHaveAttribute('tabindex', '0');
    await expect(teal).toHaveAttribute('aria-checked', 'true');
    await page.keyboard.press('Escape');
    await expect(dlg).toBeHidden();

    // Off-palette colour: nothing checked, the first tile is the Tab stop.
    await r.evaluate((el) => el.setAttribute('data-field-color', '#123456'));
    await r.click();
    await expect(dlg).toBeVisible();
    await expect(grid.locator('[aria-checked="true"]')).toHaveCount(0);
    await expect(grid.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(grid.locator('.item-color-tile').first()).toHaveAttribute('tabindex', '0');
    await page.keyboard.press('Escape');
    await expect(dlg).toBeHidden();

    // Arrow selection is a real change: Escape asks to discard.
    await page.goto('/categories');
    await row(page, name).click();
    await expect(dlg).toBeVisible();
    await teal.focus();
    const before = await page.locator('#category-color').inputValue();
    await page.keyboard.press('ArrowRight');
    await expect(page.locator('#category-color')).not.toHaveValue(before);
    await page.keyboard.press('Escape');
    await answerTillConfirm(page, 'cancel');
    await expect(dlg).toBeVisible();
    await page.keyboard.press('Escape');
    await answerTillConfirm(page, 'confirm');
    await expect(dlg).toBeHidden();
    none();
    assertClean();
  });

  test('basket order type: one Tab stop, arrows switch and focus follows the swap', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOrderTypePromptMode(page, 'top');
    try {
      await page.request.post('/api/pos/reset');
      await page.goto('/');
      const dine = page.getByTestId('order-type-dine-in');
      const take = page.getByTestId('order-type-takeaway');
      await expect(dine).toHaveAttribute('aria-checked', 'true');
      await expect(page.locator('[data-testid^="order-type-"][tabindex="0"]')).toHaveCount(1);
      await expect(dine).toHaveAttribute('tabindex', '0');
      await expect(take).toHaveAttribute('tabindex', '-1');

      await dine.focus();
      await page.keyboard.press('ArrowRight');
      await expect(take).toHaveAttribute('aria-checked', 'true');
      await expect.poll(() => page.evaluate(() => document.activeElement?.id)).toBe('order-type-takeaway');
      await expect(take).toHaveAttribute('tabindex', '0');
      await page.keyboard.press('ArrowLeft');
      await expect(dine).toHaveAttribute('aria-checked', 'true');
      await expect.poll(() => page.evaluate(() => document.activeElement?.id)).toBe('order-type-dine-in');
      // WCAG 1.4.11: the white ring alone is 1.8-2.8:1 on the accent fill, so the
      // focused active segment carries a dark inner ring beside it (two-tone).
      const ring = await dine.evaluate((el) => {
        const s = getComputedStyle(el);
        return { c: s.outlineColor, shadow: s.boxShadow };
      });
      expect(ring.c).toBe('rgb(255, 255, 255)');
      expect(ring.shadow).toMatch(/rgb\(0, 0, 0\)/);
      expect(ring.shadow).toMatch(/inset/);

      await page.keyboard.press('ArrowRight');
      await expect(take).toHaveAttribute('aria-checked', 'true');
      await page.reload();
      await expect(page.getByTestId('order-type-takeaway')).toHaveAttribute('aria-checked', 'true'); // persisted
      await page.getByTestId('order-type-takeaway').focus();
      await page.keyboard.press('ArrowLeft');
      await expect(page.getByTestId('order-type-dine-in')).toHaveAttribute('aria-checked', 'true');
    } finally {
      await page.request.post('/api/pos/reset').catch(() => {});
    }
    assertClean();
  });

  test('RTL: ArrowLeft selects the visually next colour tile', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await openNewItemForm(page);
    const tiles = page.locator(`${ITEM_GRID} .item-color-tile`);
    await tiles.nth(0).click();
    await page.keyboard.press('ArrowLeft');
    await expect(tiles.nth(1)).toHaveAttribute('aria-checked', 'true');
    await closeItemForm(page);
    assertClean();
  });

  test('focus ring on a text field is opaque and 2px', async ({ page }) => {
    await page.goto('/catalog');
    await openNewItemForm(page);
    const name = page.locator('#item-name');
    await name.focus();
    const css = await name.evaluate((el) => {
      const s = getComputedStyle(el);
      return { c: s.outlineColor, w: s.outlineWidth, st: s.outlineStyle };
    });
    expect(css.c).toMatch(/^rgb\(/);
    expect(css.w).toBe('2px');
    expect(css.st).toBe('solid');
  });
});
