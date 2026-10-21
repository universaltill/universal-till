import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#4418 (split from #3605): on an additional till every catalogue
// write travels to the main till and is refused while it can't be reached.
// #3605 disabled the forms; the gestures and entry points outside a
// fieldset[data-main-till-lock] stayed live, so an operator could drag a
// whole layout before learning nothing was saved. Now:
//   - the sale screen's quick-button edit mode doesn't open (a hold or a
//     right-click shows the lock note instead, and the hold's trailing
//     click doesn't ring the tile up), and a lock arriving mid-edit ends it;
//   - list-reorder.js lists (/categories, the Designer's categories) don't
//     drag or move by keyboard;
//   - the "+ New" buttons are disabled; the Designer says why.
// The lock is driven through the real follower, as in
// categories-record-dialog-main-till-lock-3605.spec.ts: a chip with
// data-link-state swapped into #sb-main-till-mount, which is what the 5 s
// /ui/main-till-status poll does on a replica.
//
// HONESTY NOTE: the gestures are Playwright's mouse pointer (Chromium
// PointerEvents, pointerType "mouse"), not real touch hardware.

async function flipChip(page: Page, state: string) {
  await page.evaluate((s) => {
    const mount = document.getElementById('sb-main-till-mount')!;
    mount.innerHTML = `<a class="sb-item sb-link" data-link-state="${s}">x</a>`;
    mount.dispatchEvent(new CustomEvent('htmx:afterSwap', { bubbles: true }));
  }, state);
}

// The till's own background POSTs (input heartbeat) are not a tile action.
const isAction = (r: { method(): string; url(): string }) =>
  r.method() === 'POST' && !r.url().includes('/api/window/');

async function longPress(page: Page, sel: string) {
  const box = (await page.locator(sel).first().boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(800);
  await page.mouse.up();
}

const TILE = '#buttons-grid .btn-tile[data-code]:visible';

test.describe('edit mode and reorder vs the main-till lock (ut-docs#4418)', () => {
  test('sale screen: edit mode refuses to open while locked, and ends when the lock arrives', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await expect(page.locator(TILE).first()).toBeVisible();
    const grid = page.locator('#buttons-grid');
    const note = page.getByTestId('jiggle-lock-note');
    await expect(note).toBeHidden();

    await flipChip(page, 'unreachable');
    const posts: string[] = [];
    page.on('request', (r) => { if (isAction(r)) posts.push(r.url()); });

    await page.locator(TILE).first().click({ button: 'right' });
    await expect(note).toBeVisible();
    await expect(note).toContainText("Can't reach the main till");
    await expect(grid).not.toHaveClass(/jiggle-mode/);

    // A hold neither opens the mode nor rings the tile up with its
    // trailing click.
    await longPress(page, TILE);
    await page.waitForTimeout(300);
    await expect(grid).not.toHaveClass(/jiggle-mode/);
    expect(posts, 'a refused hold sends nothing').toEqual([]);

    // A plain tap still sells while locked (offline-first: never blocked).
    const sold = page.waitForRequest(isAction);
    await page.locator(TILE).first().click();
    await sold;

    // The main till is back: the note goes, the mode opens again.
    await flipChip(page, 'linked');
    await expect(note).toBeHidden();
    await page.locator(TILE).first().click({ button: 'right' });
    await expect(grid).toHaveClass(/jiggle-mode/);
    await expect(page.getByTestId('jiggle-bar')).toBeVisible();

    // The lock arriving mid-edit ends the mode.
    await flipChip(page, 'unreachable');
    await expect(grid).not.toHaveClass(/jiggle-mode/);
    await expect(page.getByTestId('jiggle-bar')).toBeHidden();
    // The chip going away (this till promoted to main till) lifts the
    // lock too — nothing else would clear <html>'s attribute (review F1).
    await page.evaluate(() => {
      const mount = document.getElementById('sb-main-till-mount')!;
      mount.innerHTML = '';
      mount.dispatchEvent(new CustomEvent('htmx:afterSwap', { bubbles: true }));
    });
    await expect(page.locator('html')).not.toHaveAttribute('data-main-till-locked', /.*/);
    await page.locator(TILE).first().click({ button: 'right' });
    await expect(grid).toHaveClass(/jiggle-mode/);
    await page.getByTestId('jiggle-done').click();
    await page.request.post('/api/pos/reset');
    assertClean();
  });

  test('/categories: New is disabled and the list neither drags nor moves by keyboard', async ({ page }) => {
    await page.goto('/categories');
    const rows = page.locator('#categories-table .category-row');
    test.skip((await rows.count()) < 2, 'needs two categories in the demo catalog');
    const order = () => rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-id')));
    const before = await order();
    await expect(page.locator('#categories-new')).toBeEnabled();

    await flipChip(page, 'unreachable');
    await expect(page.locator('#categories-new')).toBeDisabled();
    const posts: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/reorder')) posts.push(r.url()); });

    // Long-press the first row and drag it below the second.
    const a = (await rows.nth(0).boundingBox())!;
    const b = (await rows.nth(1).boundingBox())!;
    await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(700);
    await page.mouse.move(b.x + b.width / 2, b.y + b.height - 2, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.press('Escape');
    // Alt+ArrowDown on the first row's button.
    await page.locator('#categories-table .category-row-open').first().focus();
    await page.keyboard.press('Alt+ArrowDown');
    await page.waitForTimeout(300);
    expect(await order()).toEqual(before);
    expect(posts, 'no reorder save is attempted').toEqual([]);

    await flipChip(page, 'linked');
    await expect(page.locator('#categories-new')).toBeEnabled();
  });

  test('Designer and catalogue: New buttons disabled, the Designer says why', async ({ page }) => {
    await page.goto('/designer');
    const newCat = page.locator('#designer-cat-new');
    await expect(newCat).toBeEnabled();
    await expect(page.getByTestId('designer-lock-note')).toBeHidden();
    await flipChip(page, 'unreachable');
    await expect(newCat).toBeDisabled();
    await expect(page.getByTestId('designer-lock-note')).toBeVisible();
    await flipChip(page, 'linked');
    await expect(newCat).toBeEnabled();
    await expect(page.getByTestId('designer-lock-note')).toBeHidden();

    await page.goto('/catalog');
    await expect(page.locator('#item-form-add-btn')).toBeEnabled();
    await flipChip(page, 'unreachable');
    await expect(page.locator('#item-form-add-btn')).toBeDisabled();
    await expect(page.locator('#item-form-reset')).toBeDisabled();
    await flipChip(page, 'linked');
    await expect(page.locator('#item-form-add-btn')).toBeEnabled();
  });
});
