import { test, expect } from './fixtures';
import type { Page, Locator, CDPSession } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2699: both category lists — /categories and the Designer's
// category-management section — lose the per-row pencil and up/down
// chevrons. A tap still edits; a long-press (~450ms, <8px movement) lifts
// the row and a drag reorders it (web/public/list-reorder.js), saved
// through the SAME endpoints as before (/api/categories/reorder,
// /api/designer/categories/reorder); Escape cancels; Alt+ArrowUp/Down on a
// focused row moves it with an aria-live announcement; Move up/Move down
// live in the edit dialog / inline form as the single-pointer alternative.
// Every order assertion is made AFTER A RELOAD: the DOM moves before the
// request, so "the rows moved" alone would pass against a refused save
// (the ut-docs#2018 lesson).
//
// HONESTY NOTE (the `ux` skill's touch rule): the mouse tests drive
// Chromium's real PointerEvents with pointerType "mouse"; the touch tests
// dispatch CDP Input.dispatchTouchEvent, i.e. Chromium's own touch
// pipeline (pointerType "touch", touch-action, touchmove cancelation) —
// emulated touch in desktop Chromium, NOT a finger on the pilot till's
// WebKitGTK kiosk. That remains the local hardware lane's check.

const RUN = Date.now().toString(36).toUpperCase();
const DIALOG = '#category-dialog';

async function clickThenReload(page: Page, click: () => Promise<void>) {
  await Promise.all([page.waitForEvent('load'), click()]);
}

async function createCategory(page: Page, name: string) {
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator('#category-form input[name="name"]').fill(name);
  await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
  await expect(page.locator('#categories-table .category-row', { hasText: name })).toHaveCount(1);
}

function catRow(page: Page, name: string) {
  return page.locator('#categories-table .category-row', { hasText: name }).first();
}

// Index of each needle in the list's current DOM order.
async function orderOf(list: Locator, names: string[]): Promise<number[]> {
  const all = await list.locator('[data-reorder-id]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('data-reorder-name') || ''));
  return names.map((n) => all.indexOf(n));
}

async function center(el: Locator) {
  await el.scrollIntoViewIfNeeded();
  const b = (await el.boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
}

// Long-press `from`, then drag it to just inside the top edge of `over`
// (so it lands BEFORE `over`), in small steps like a finger.
async function mouseLongPressDrag(page: Page, from: Locator, over: Locator) {
  const a = await center(from);
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.waitForTimeout(650);
  const ob = (await over.boundingBox())!;
  await page.mouse.move(a.x, ob.y + 3, { steps: 20 });
  await page.mouse.up();
}

async function touch(cdp: CDPSession, type: 'touchStart' | 'touchMove' | 'touchEnd', x: number, y: number) {
  await cdp.send('Input.dispatchTouchEvent', {
    type,
    touchPoints: type === 'touchEnd' ? [] : [{ x: Math.round(x), y: Math.round(y) }],
  });
}

async function touchLongPressDrag(page: Page, from: Locator, over: Locator) {
  const cdp = await page.context().newCDPSession(page);
  const a = await center(from);
  await touch(cdp, 'touchStart', a.x, a.y);
  await page.waitForTimeout(650);
  const ob = (await over.boundingBox())!;
  const steps = 15;
  for (let i = 1; i <= steps; i++) {
    await touch(cdp, 'touchMove', a.x, a.y + ((ob.y + 3) - a.y) * (i / steps));
  }
  await touch(cdp, 'touchEnd', 0, 0);
  await cdp.detach();
}

function countRequests(page: Page, urlPart: string) {
  const seen: string[] = [];
  page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes(urlPart)) seen.push(r.url()); });
  return seen;
}

test.describe('/categories: long-press drag reorder (ut-docs#2699)', () => {
  async function seed(page: Page, tag: string) {
    await page.goto('/categories');
    const n = [`Drag${tag} A ${RUN}`, `Drag${tag} B ${RUN}`, `Drag${tag} C ${RUN}`];
    for (const name of n) await createCategory(page, name);
    return n;
  }

  test('rows carry no pencil or chevrons; a tap opens the editor and saves nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a] = await seed(page, 'Tap');
    const table = page.locator('#categories-table');
    await expect(table.locator('[data-icon="pencil"], [data-icon="chevron-up"], [data-icon="chevron-down"]')).toHaveCount(0);
    await expect(table.locator('.category-row .cat-thumb').first()).toBeVisible();
    // touch-action does not apply to a <tr>: the cells must carry pan-y so
    // a vertical swipe still scrolls and the browser leaves the rest to us.
    expect(await catRow(page, a).locator('td').first().evaluate((el) => getComputedStyle(el).touchAction)).toBe('pan-y');
    const posts = countRequests(page, '/reorder');
    await catRow(page, a).locator('td').nth(1).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#category-form input[name="name"]')).toHaveValue(a);
    await page.waitForTimeout(300);
    expect(posts).toEqual([]);
    assertClean();
  });

  test('long-press + drag (mouse) moves a row and the order survives a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b, c] = await seed(page, 'Mouse');
    const list = page.locator('#categories-table tbody');
    const saved = page.waitForResponse((r) => r.url().includes('/api/categories/reorder') && r.request().method() === 'POST');
    await mouseLongPressDrag(page, catRow(page, c), catRow(page, a));
    expect((await saved).status()).toBe(204);
    // The drop's trailing click must not have opened the editor.
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(page.locator('#categories-reorder-live')).toContainText(`Moved ${c} to position`);
    await page.reload();
    const [ia, ib, ic] = await orderOf(list, [a, b, c]);
    expect(ic, 'C now sits before A').toBe(ia - 1);
    expect(ib).toBe(ia + 1);
    assertClean();
  });

  test('long-press + drag (touch) moves a row and the order survives a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b, c] = await seed(page, 'Touch');
    const list = page.locator('#categories-table tbody');
    const saved = page.waitForResponse((r) => r.url().includes('/api/categories/reorder') && r.request().method() === 'POST');
    await touchLongPressDrag(page, catRow(page, c), catRow(page, a));
    expect((await saved).status()).toBe(204);
    await expect(page.locator(DIALOG)).toBeHidden();
    await page.reload();
    const [ia, ib, ic] = await orderOf(list, [a, b, c]);
    expect(ic).toBe(ia - 1);
    expect(ib).toBe(ia + 1);
    assertClean();
  });

  test('a touch swipe before the hold arms never lifts the row (it scrolls instead)', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [, , c] = await seed(page, 'Swipe');
    const posts = countRequests(page, '/reorder');
    const cdp = await page.context().newCDPSession(page);
    const p = await center(catRow(page, c));
    await touch(cdp, 'touchStart', p.x, p.y);
    for (let i = 1; i <= 6; i++) await touch(cdp, 'touchMove', p.x, p.y - i * 10);
    await page.waitForTimeout(650);
    await expect(page.locator('#categories-table .is-dragging')).toHaveCount(0);
    await touch(cdp, 'touchEnd', 0, 0);
    await cdp.detach();
    await page.waitForTimeout(300);
    expect(posts).toEqual([]);
    await expect(page.locator(DIALOG)).toBeHidden();
    assertClean();
  });

  test('Escape during a drag puts the rows back and saves nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b, c] = await seed(page, 'Esc');
    const list = page.locator('#categories-table tbody');
    const before = await orderOf(list, [a, b, c]);
    const posts = countRequests(page, '/reorder');
    const from = await center(catRow(page, c));
    await page.mouse.move(from.x, from.y);
    await page.mouse.down();
    await page.waitForTimeout(650);
    const ob = (await catRow(page, a).boundingBox())!;
    await page.mouse.move(from.x, ob.y + 3, { steps: 15 });
    expect((await orderOf(list, [a, b, c]))[2]).toBeLessThan(before[2]); // it really moved mid-drag
    await page.keyboard.press('Escape');
    await page.mouse.up();
    expect(await orderOf(list, [a, b, c])).toEqual(before);
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(page.locator('#categories-reorder-live')).toContainText('cancelled');
    await page.waitForTimeout(300);
    expect(posts).toEqual([]);
    // The cancelled gesture's owed click never arrives (its row moved in
    // the DOM); the NEXT tap must still open the editor, not be eaten.
    await catRow(page, b).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    assertClean();
  });

  test('Alt+ArrowDown on a focused row moves it, announces it, and persists', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b] = await seed(page, 'Kbd');
    const list = page.locator('#categories-table tbody');
    const btn = catRow(page, a).locator('.category-row-open');
    await btn.focus();
    const saved = page.waitForResponse((r) => r.url().includes('/api/categories/reorder') && r.request().method() === 'POST');
    await page.keyboard.press('Alt+ArrowDown');
    expect((await saved).status()).toBe(204);
    await expect(btn).toBeFocused();
    await expect(page.locator('#categories-reorder-live')).toContainText(`Moved ${a} to position`);
    await expect(page.locator(DIALOG)).toBeHidden();
    await page.reload();
    const [ia, ib] = await orderOf(list, [a, b]);
    expect(ia).toBe(ib + 1);
    assertClean();
  });

  test('Move up in the edit dialog persists and updates its position line', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b] = await seed(page, 'Dlg');
    const list = page.locator('#categories-table tbody');
    await catRow(page, b).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    const pos = page.locator('#category-move-position');
    const before = await pos.textContent();
    const saved = page.waitForResponse((r) => r.url().includes('/api/categories/reorder') && r.request().method() === 'POST');
    await page.locator('#category-move-up').click();
    expect((await saved).status()).toBe(204);
    await expect(pos).not.toHaveText(before || '');
    await expect(page.locator(DIALOG)).toBeVisible(); // stays open for another move
    await page.reload();
    const [ia, ib] = await orderOf(list, [a, b]);
    expect(ib).toBe(ia - 1);
    assertClean();
  });

  // Review fix: with the search filter active, move() steps over VISIBLE
  // neighbours only, so the dialog's "Position N of M" and the announcement
  // must count the visible rows too — otherwise one Move up press could
  // jump "Position 3 of 12" to "1 of 12" (hidden rows skipped) while the
  // user saw a two-row list.
  test('with the search filter active, Move up counts, announces and persists over the visible rows', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const keep = `Keep${RUN}`;
    const a = `${keep} A`, x = `Skip X ${RUN}`, b = `${keep} B`;
    for (const name of [a, x, b]) await createCategory(page, name);
    const list = page.locator('#categories-table tbody');
    const [ia0, ix0, ib0] = await orderOf(list, [a, x, b]);
    expect(ia0).toBeLessThan(ix0);
    expect(ix0).toBeLessThan(ib0); // X (hidden below) sits between A and B
    await page.locator('#categories-search').fill(keep);
    await expect(catRow(page, x)).toBeHidden();
    await expect(page.locator('#categories-table .category-row:visible')).toHaveCount(2);

    await catRow(page, b).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    const pos = page.locator('#category-move-position');
    await expect(pos).toHaveText('Position 2 of 2');
    await expect(page.locator('#category-move-up')).toBeEnabled();
    await expect(page.locator('#category-move-down')).toBeDisabled();
    const saved = page.waitForResponse((r) => r.url().includes('/api/categories/reorder') && r.request().method() === 'POST');
    await page.locator('#category-move-up').click();
    expect((await saved).status()).toBe(204);
    await expect(pos).toHaveText('Position 1 of 2');
    await expect(page.locator('#category-move-up')).toBeDisabled();
    await expect(page.locator('#categories-reorder-live')).toHaveText(`Moved ${b} to position 1 of 2.`);

    // One press stepped over the hidden X to land just above A.
    await page.reload();
    const [ia, ix, ib] = await orderOf(list, [a, x, b]);
    expect(ib, 'B now sits directly before A').toBe(ia - 1);
    expect(ix, 'the hidden X kept its place after A').toBe(ia + 1);
    assertClean();
  });

  test('a refused save puts the rows back and says why', async ({ page }) => {
    const assertClean = watchConsole(page, /409/);
    const [a, b, c] = await seed(page, 'Fail');
    const list = page.locator('#categories-table tbody');
    const before = await orderOf(list, [a, b, c]);
    await page.route('**/api/categories/reorder', (route) => route.fulfill({ status: 409, contentType: 'text/plain', body: 'Manage categories on the main till.' }));
    await catRow(page, a).locator('.category-row-open').focus();
    await page.keyboard.press('Alt+ArrowDown');
    await expect.poll(() => orderOf(list, [a, b, c])).toEqual(before);
    await expect(page.locator('#categories-reorder-msg')).toBeVisible();
    await expect(page.locator('#categories-reorder-msg')).toContainText('main till');
    assertClean();
  });
});

test.describe('Designer category list: long-press drag reorder (ut-docs#2699)', () => {
  async function seed(page: Page, tag: string) {
    const n = [`DDrag${tag} A ${RUN}`, `DDrag${tag} B ${RUN}`, `DDrag${tag} C ${RUN}`];
    for (const name of n) {
      const res = await page.request.post('/api/designer/categories', { form: { name } });
      expect(res.status(), `create ${name}`).toBe(204);
    }
    await page.goto('/designer');
    await expect(page.locator('.designer-cat', { hasText: n[2] })).toBeVisible();
    return n;
  }
  function row(page: Page, name: string) {
    return page.locator('.designer-cat', { hasText: name }).first();
  }
  const list = (page: Page) => page.locator('.designer-cat-list');

  test('rows carry no pencil or chevrons; tapping the row body toggles its form', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a] = await seed(page, 'Tap');
    const r = row(page, a);
    await expect(r.locator('.designer-cat-actions [data-icon="pencil"], .designer-cat-actions [data-icon^="chevron"], .designer-cat-open [data-icon^="chevron"]')).toHaveCount(0);
    await expect(r.locator('.designer-cat-actions button')).toHaveCount(1); // deactivate only
    const posts = countRequests(page, '/reorder');
    const open = r.locator('.designer-cat-open');
    await expect(open).toHaveAttribute('aria-expanded', 'false');
    await open.click();
    await expect(open).toHaveAttribute('aria-expanded', 'true');
    await expect(r.locator('.designer-cat-form')).toBeVisible();
    await page.waitForTimeout(300);
    expect(posts).toEqual([]);
    assertClean();
  });

  test('long-press + drag moves a category and the order survives a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b, c] = await seed(page, 'Mouse');
    const saved = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');
    await mouseLongPressDrag(page, row(page, c).locator('.designer-cat-open'), row(page, a));
    expect((await saved).status()).toBe(204);
    await expect(row(page, c).locator('.designer-cat-form')).toBeHidden(); // the drop did not toggle the form
    await page.reload();
    await expect(row(page, c)).toBeVisible();
    const [ia, ib, ic] = await orderOf(list(page), [a, b, c]);
    expect(ic).toBe(ia - 1);
    expect(ib).toBe(ia + 1);
    assertClean();
  });

  test('long-press + drag (touch) persists', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b, c] = await seed(page, 'Touch');
    const saved = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');
    await touchLongPressDrag(page, row(page, c).locator('.designer-cat-open'), row(page, a));
    expect((await saved).status()).toBe(204);
    await page.reload();
    await expect(row(page, c)).toBeVisible();
    const [ia, ib, ic] = await orderOf(list(page), [a, b, c]);
    expect(ic).toBe(ia - 1);
    expect(ib).toBe(ia + 1);
    assertClean();
  });

  test('Alt+ArrowDown persists and focus survives the re-render', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b] = await seed(page, 'Kbd');
    const id = (await row(page, a).getAttribute('data-cat-id'))!;
    await page.locator(`#designer-cat-open-${id}`).focus();
    const saved = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');
    await page.keyboard.press('Alt+ArrowDown');
    expect((await saved).status()).toBe(204);
    await expect.poll(() => page.evaluate(() => document.activeElement && document.activeElement.id)).toBe(`designer-cat-open-${id}`);
    await page.reload();
    await expect(row(page, a)).toBeVisible();
    const [ia, ib] = await orderOf(list(page), [a, b]);
    expect(ia).toBe(ib + 1);
    assertClean();
  });

  test('Move later in the inline form persists and the form stays open', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [a, b] = await seed(page, 'Form');
    const id = (await row(page, a).getAttribute('data-cat-id'))!;
    await page.locator(`#designer-cat-open-${id}`).click();
    const down = page.locator(`#designer-cat-down-${id}`);
    await expect(down).toBeVisible();
    await down.focus();
    const saved = page.waitForResponse((r) => r.url().endsWith('/api/designer/categories/reorder') && r.request().method() === 'POST');
    await page.keyboard.press('Enter');
    expect((await saved).status()).toBe(204);
    await expect(page.locator(`#designer-cat-form-${id}`)).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.activeElement && document.activeElement.id)).toBe(`designer-cat-down-${id}`);
    await page.reload();
    await expect(row(page, a)).toBeVisible();
    const [ia, ib] = await orderOf(list(page), [a, b]);
    expect(ia).toBe(ib + 1);
    assertClean();
  });
});
