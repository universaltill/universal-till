import { test, expect } from './fixtures';
import type { Page, Locator, Request } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#2339: a long-press (~500ms hold, cancelled by >10px movement) or
// a right-click on a sell-screen tile puts the WHOLE quick-button grid into
// an iOS-springboard-style edit mode (#buttons-grid.jiggle-mode): every
// tile wobbles, shows an edit badge (leading top corner) and a trash badge
// (trailing top corner), and can be dragged to reorder; Done / Escape / a
// tap outside the grid exits and persists the new order with ONE POST to
// /api/buttons/reorder. This replaced the ut-docs#2285 per-tile sheet
// (sell-tile-long-press-2285.spec.ts, retired with it).
//
// ut-docs#2541: since every active, non-hidden catalog item is a quick
// button by default now, the trash badge's OWN meaning changed — it no
// longer just removes a shortcut_buttons row (the tile would just come
// back), it deletes the ITEM itself (POST /api/buttons/delete-item, the
// same soft-deactivate the catalog page's own "Delete item" uses). A third
// badge (trailing BOTTOM corner, eye-off icon) now hides a tile from the
// sell screen without touching the catalog — this file's own "Remove
// badge" step below is rewritten into a "Hide badge" step for that reason;
// destructive delete-item coverage lives in the Go-level handler tests
// (internal/pages/buttons_hide_api_test.go), not here.
//
// HONESTY NOTE (per the `ux` skill's touch-sensitive-change rule, same
// convention as designer-reorder-1221.spec.ts's own note): every gesture
// below is driven via Playwright's synthetic mouse pointer (page.mouse),
// which Chromium's own input pipeline turns into real PointerEvents with
// pointerType "mouse" — not real touch hardware, not WebKitGTK. What this
// file proves: the hold timing, the click-swallow, the pointer-capture drag
// + midpoint reorder logic, the persisted order, and the badge actions all
// work end to end against the real server in Chromium. What it cannot
// prove: touch-action/pointer-capture behaviour under a real finger on the
// pilot till's WebKitGTK kiosk (palm rejection, a second finger, the
// products panel's own scroll competing with the drag) — that's the local
// hardware lane's job, exactly as tables.html's own drag pattern records.

// Per-run suffix: cleanupItems() DEACTIVATES the fixture items, and a
// deactivated item no longer renders a .catalog-row, so a second run of
// this spec against the same still-running till (a local re-run after a
// failure — CI boots a fresh DB per run) could never re-seed under the
// same SKU. Fresh names/SKUs/barcodes per run sidestep that entirely.
// The same trap applies BETWEEN the two tests in this file: each gets its
// own suffix (fixture('1') / fixture('2')), since the first test's cleanup
// deactivates its items and a second import under the same SKUs would
// update those deactivated rows in place -- no .catalog-row, seedItems
// times out (found live on this file's first full run).
const RUN = Date.now().toString(36).toUpperCase();
function fixture(tag: string) {
  const run = `${RUN}${tag}`;
  const cat = `Jiggle2339 Cat ${run}`;
  return {
    run,
    A: { name: `Jiggle2339 Item A ${run}`, sku: `JIG2339A${run}`, barcode: `JIG2339BC-A-${run}`, category: cat },
    B: { name: `Jiggle2339 Item B ${run}`, sku: `JIG2339B${run}`, barcode: `JIG2339BC-B-${run}`, category: cat },
    C: { name: `Jiggle2339 Item C ${run}`, sku: `JIG2339C${run}`, barcode: `JIG2339BC-C-${run}`, category: cat },
  };
}
type Item = ReturnType<typeof fixture>['A'];

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Mirrors category-switch-stale-tile-add-1433.spec.ts's own seedItems: a
// plain catalog import only creates `items` rows, so each item is ALSO
// added as a sell-screen shortcut via /api/buttons/add so it renders as a
// tile. All three share one category, so they're siblings in ONE .grid —
// the unit a drag reorders within.
async function seedItems(page: Page, items: Item[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: 'import-2339.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(csvFor(items)),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    const id = (await row.first().getAttribute('data-id'))!;
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id, label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
}

async function cleanupItems(page: Page, items: Item[]) {
  // ut-docs#2541: /api/buttons/remove now HIDES the item rather than just
  // deleting its shortcut_buttons row — harmless here, since the very next
  // step deactivates the item outright anyway.
  for (const it of items) {
    await page.request.post('/api/buttons/remove', { form: { code: it.barcode } });
  }
  await page.goto('/catalog');
  for (const it of items) {
    const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
    if ((await row.count()) === 0) continue;
    const id = await row.first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

async function center(el: Locator): Promise<{ x: number; y: number }> {
  const box = (await el.boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

// A real long-press: pointerdown, hold past app.js's 500ms threshold, then
// release without moving — the mode-entering path.
async function longPress(tile: Locator) {
  const page = tile.page();
  const c = await center(tile);
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

// Drag `tile` and drop it just past the trailing midpoint of `over`, in
// small steps so app.js's per-move hit-test sees the pointer cross each
// intermediate sibling's midpoint (a single jump would still land, but
// stepping is what a finger actually does).
async function dragPast(tile: Locator, over: Locator) {
  const page = tile.page();
  const from = await center(tile);
  const toBox = (await over.boundingBox())!;
  const to = { x: toBox.x + toBox.width * 0.8, y: toBox.y + toBox.height / 2 };
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(from.x + 6, from.y + 2, { steps: 3 }); // clears the 3px drag-start jitter band
  await page.mouse.move(to.x, to.y, { steps: 25 });
  await page.mouse.up();
}

const grid = (page: Page) => page.locator('#buttons-grid');
const codesInOrder = (page: Page, run: string) =>
  page
    .locator(`.products-tab-panel .btn-tile[data-code^="JIG2339BC-"][data-code$="-${run}"]`)
    .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code));

test.describe('Sell-screen jiggle edit mode (ut-docs#2339)', () => {
  // ut-docs#2499: the category strip (and its "..." overflow) is now ONE of
  // three selectable sell-screen browsing modes (Settings -> Sell screen,
  // sale.browsing_mode) and no longer the unconditional default (that is
  // category tiles) -- this file is about the strip, so it picks
  // strip_overflow explicitly rather than assuming it. worker-till.ts
  // already boots every worker till in this mode; this is what makes the
  // dependency visible in the spec itself.
  test.beforeEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
  });
  test('long-press enters, drag reorders, Done persists once, badges edit/remove', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { run: RUN1, A: ITEM_A, B: ITEM_B, C: ITEM_C } = fixture('1');
    const ALL = [ITEM_A, ITEM_B, ITEM_C];
    await seedItems(page, ALL);

    // Every request the mode makes to the buttons API, in order — the
    // offline-first contract is "zero calls until Done, then exactly one".
    const buttonCalls: Request[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/buttons/')) buttonCalls.push(r);
    });

    try {
      await page.goto('/');
      // The strip's default tab is its first category (ut-docs#2613 retired
      // the All tab), not necessarily this fixture's own -- switch to the
      // fixture's category tab so its `.products-tab-panel` is the visible
      // one, not just DOM-present-but-hidden.
      await page.getByRole('tab', { name: ITEM_A.category }).click();
      const tileA = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`);
      const tileB = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_B.barcode}"]`);
      const tileC = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_C.barcode}"]`);
      await expect(tileA).toBeVisible();
      await expect(tileB).toBeVisible();
      await expect(tileC).toBeVisible();
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_A.barcode, ITEM_B.barcode, ITEM_C.barcode]);

      // (0) At rest: no badges, no bar, no .jiggle-mode — the resting grid
      // is what it was before this card.
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeHidden();
      await expect(tileA.locator('xpath=..').locator('[data-testid="tile-badge-edit"]')).toBeHidden();

      // (1) A plain click still adds to the basket — the hold machinery
      // must never swallow an ordinary tap.
      await tileA.click();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await expect(page.locator('.basket .line-name').first()).toHaveText(ITEM_A.name);

      // (2) A real long-press enters the mode instead of adding: the grid
      // gets .jiggle-mode, the Done bar shows, every tile shows its two
      // badges, the basket is untouched — and NOTHING was requested.
      buttonCalls.length = 0;
      await longPress(tileB);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeVisible();
      await expect(page.locator('[data-testid="jiggle-done"]')).toBeVisible();
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      const cellB = tileB.locator('xpath=..');
      const editB = cellB.locator('[data-testid="tile-badge-edit"]');
      const removeB = cellB.locator('[data-testid="tile-badge-remove"]');
      await expect(editB).toBeVisible();
      await expect(removeB).toBeVisible();
      expect(buttonCalls, 'entering the mode must make no request').toHaveLength(0);

      // (2a) Badge geometry: the hit area honours the 44px touch floor
      // (ut-docs#161) even though the visible dot is small, edit sits at
      // the inline-START corner and remove at the inline-END corner (LTR
      // here: start = left), and both sit on the tile's top edge.
      const bB = (await tileB.boundingBox())!;
      const eB = (await editB.boundingBox())!;
      const rB = (await removeB.boundingBox())!;
      expect(eB.width).toBeGreaterThanOrEqual(44);
      expect(eB.height).toBeGreaterThanOrEqual(44);
      expect(rB.width).toBeGreaterThanOrEqual(44);
      expect(rB.height).toBeGreaterThanOrEqual(44);
      expect(eB.x + eB.width / 2).toBeLessThan(bB.x + bB.width / 2);
      expect(rB.x + rB.width / 2).toBeGreaterThan(bB.x + bB.width / 2);
      expect(eB.y).toBeLessThan(bB.y + 4);
      // The wobble is wired (animation-name) — the fixture runs every page
      // as a reduced-motion user, where the global reduced-motion rule
      // zeroes its duration; both halves asserted, since the animation
      // must exist AND must be killed under that preference.
      await expect
        .poll(() => tileB.evaluate((el) => getComputedStyle(el).animationName))
        .toBe('ut-jiggle');
      expect(await tileB.evaluate((el) => getComputedStyle(el).animationDuration)).toBe('0s');
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      expect(await tileB.evaluate((el) => getComputedStyle(el).animationDuration)).not.toBe('0s');
      await page.emulateMedia({ reducedMotion: 'reduce' });

      // (2b) A tap on a jiggling tile does NOT add it to the basket.
      await tileC.click();
      await page.waitForTimeout(300); // give a leaked click time to reach /api/pos/scan
      await expect(page.locator('.basket .line-name')).toHaveCount(1);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);

      // (3) Escape exits with nothing to save: no reorder POST at all.
      await page.keyboard.press('Escape');
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeHidden();
      await page.waitForTimeout(200);
      expect(buttonCalls.filter((r) => r.url().includes('/api/buttons/reorder'))).toHaveLength(0);

      // (3a) A press cancelled by movement neither enters the mode nor
      // adds a basket line (moves past the tile's own edge so the mouseup
      // lands outside it entirely — same reasoning as the 2285 spec).
      const boxB = (await tileB.boundingBox())!;
      await page.mouse.move(boxB.x + boxB.width / 2, boxB.y + boxB.height / 2);
      await page.mouse.down();
      await page.mouse.move(boxB.x + boxB.width + 40, boxB.y + boxB.height / 2);
      await page.waitForTimeout(700);
      await page.mouse.up();
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('.basket .line-name')).toHaveCount(1);

      // (4) Drag A past C (crossing B's and then C's midpoints) — the DOM
      // reflows live to [B, C, A] with still no request; Done then fires
      // EXACTLY ONE reorder POST whose body is the full ordered code list,
      // and the order survives a reload.
      await longPress(tileA);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      buttonCalls.length = 0;
      await dragPast(tileA, tileC);
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_B.barcode, ITEM_C.barcode, ITEM_A.barcode]);
      expect(buttonCalls, 'dragging must make no request').toHaveLength(0);

      const reorderResponse = page.waitForResponse(
        (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
      );
      await page.locator('[data-testid="jiggle-done"]').click();
      const res = await reorderResponse;
      expect(res.status()).toBe(204);
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(200);
      const reorders = buttonCalls.filter((r) => r.url().includes('/api/buttons/reorder'));
      expect(reorders, 'Done must persist with exactly one POST').toHaveLength(1);
      const posted = new URLSearchParams(reorders[0].postData() || '').getAll('codes');
      // The posted list is the FULL global order (demo tiles included);
      // our three must appear in the new order, and the posted list must
      // be a permutation of every tile on screen.
      const ours = posted.filter((c) => c.startsWith('JIG2339BC-') && c.endsWith(`-${RUN1}`));
      expect(ours).toEqual([ITEM_B.barcode, ITEM_C.barcode, ITEM_A.barcode]);
      const onScreen = await page
        .locator('.products-tab-panel .btn-tile[data-code]')
        .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code).sort());
      expect([...posted].sort()).toEqual(onScreen);

      await page.reload();
      // A reload is a fresh document -- Alpine re-inits `tab` to its
      // default (All, ut-docs#2294), so the fixture's own panel must be
      // re-selected before the next longPress/focus needs it visible.
      await page.getByRole('tab', { name: ITEM_A.category }).click();
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_B.barcode, ITEM_C.barcode, ITEM_A.barcode]);

      // (4a) Keyboard parity: in the mode, ArrowLeft on a focused tile
      // moves it one place earlier; Done persists it. Restores [A, B, C].
      const tileA2 = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`);
      await longPress(tileA2);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await tileA2.focus();
      await page.keyboard.press('ArrowLeft');
      await page.keyboard.press('ArrowLeft');
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_A.barcode, ITEM_B.barcode, ITEM_C.barcode]);
      const restore = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder'));
      await page.locator('[data-testid="jiggle-done"]').click();
      expect((await restore).status()).toBe(204);
      await page.reload();
      await page.getByRole('tab', { name: ITEM_A.category }).click();
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_A.barcode, ITEM_B.barcode, ITEM_C.barcode]);

      // (5) Edit badge → the catalog item dialog with return=/; closing it
      // comes back to the sale screen.
      await longPress(page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`));
      const editA = page
        .locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`)
        .locator('xpath=..')
        .locator('[data-testid="tile-badge-edit"]');
      await expect(editA).toBeVisible();
      await editA.click();
      await expect(page).toHaveURL(/\/catalog\?item=[^&]+&return=(%2F|\/)$/);
      await expect(page.locator('#item-form-modal')).toBeVisible();
      await page.locator('#item-form-close-btn').click();
      await expect(page).toHaveURL(/\/$/);
      await page.getByRole('tab', { name: ITEM_A.category }).click();

      // (5a) The edit badge is a plain <a href>, so following it tears this
      // document down — an unsaved drag must be persisted FIRST, not
      // silently dropped. Independent review (2026-09-17) found it was:
      // drag, tap the pencil, and the reorder vanished with ZERO reorder
      // POSTs (the remove badge and a grid refetch were both already
      // handled; a navigation was the one gap). Reorder to [B, A, C], leave
      // via the pencil, and assert the order survives the round trip.
      const tileB2 = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_B.barcode}"]`);
      await longPress(page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`));
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      buttonCalls.length = 0;
      await dragPast(page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`), tileB2);
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_B.barcode, ITEM_A.barcode, ITEM_C.barcode]);
      const navSave = page.waitForResponse(
        (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
      );
      await page
        .locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`)
        .locator('xpath=..')
        .locator('[data-testid="tile-badge-edit"]')
        .click();
      expect((await navSave).status()).toBe(204);
      await expect(page).toHaveURL(/\/catalog\?item=/);
      expect(
        buttonCalls.filter((r) => r.url().includes('/api/buttons/reorder')),
        'leaving via the edit badge persists with exactly one POST',
      ).toHaveLength(1);
      await page.goto('/');
      await page.getByRole('tab', { name: ITEM_A.category }).click();
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_B.barcode, ITEM_A.barcode, ITEM_C.barcode]);
      // Put [A, B, C] back so step (6)'s counts read as before.
      await longPress(page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`));
      await page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`).focus();
      await page.keyboard.press('ArrowLeft');
      await expect.poll(() => codesInOrder(page, RUN1)).toEqual([ITEM_A.barcode, ITEM_B.barcode, ITEM_C.barcode]);
      const restore2 = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder'));
      await page.locator('[data-testid="jiggle-done"]').click();
      expect((await restore2).status()).toBe(204);
      await page.reload();
      await page.getByRole('tab', { name: ITEM_A.category }).click();

      // (6) Hide badge — LAST, and only on this spec's OWN fixture tile
      // (ut-docs#2541): no confirm dialog (reversible); POSTs
      // /api/buttons/hide; the grid refreshes WITHOUT the tile and stays in
      // edit mode (iOS keeps jiggling after a hide); Done then has nothing
      // to save. Done first via a fresh entry so the count is taken at rest.
      const beforeCount = await page.locator('.products-tab-panel .btn-tile[data-code]').count();
      const tileC2 = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_C.barcode}"]`);
      const hiddenItemId = await tileC2.getAttribute('data-item-id');
      await longPress(tileC2);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      const hideResponse = page.waitForResponse((r) => r.url().includes('/api/buttons/hide'));
      await tileC2.locator('xpath=..').locator('[data-testid="tile-badge-hide"]').click();
      await hideResponse;
      await expect(page.locator('.products-tab-panel .btn-tile[data-code]')).toHaveCount(beforeCount - 1);
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      buttonCalls.length = 0;
      await page.locator('[data-testid="jiggle-done"]').click();
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(200);
      expect(buttonCalls.filter((r) => r.url().includes('/api/buttons/reorder')), 'nothing to save after a hide').toHaveLength(0);

      // Unhide restores it as an implicit tile (ut-docs#2541) — no re-add
      // needed, unlike the retired remove-badge behavior this replaces.
      const unhide = await page.request.post('/api/buttons/unhide', {
        form: { itemId: hiddenItemId ?? '' },
      });
      expect(unhide.ok(), 'unhide the hidden tile').toBe(true);

      assertClean();
    } finally {
      await cleanupItems(page, ALL);
    }
  });

  test('a tap outside the grid exits and persists; right-click enters', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { run: RUN2, A: ITEM_A, B: ITEM_B } = fixture('2');
    await seedItems(page, [ITEM_A, ITEM_B]);
    try {
      await page.goto('/');
      // See the first test's own comment: switch to this fixture's own
      // category panel so it is what actually renders visible, not just
      // present-but-hidden in the DOM.
      await page.getByRole('tab', { name: ITEM_A.category }).click();
      const tileA = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_A.barcode}"]`);
      const tileB = page.locator(`.products-tab-panel .btn-tile[data-code="${ITEM_B.barcode}"]`);
      await expect(tileA).toBeVisible();

      // Right-click (desktop, and Android's long-press-to-contextmenu).
      // The basket is reset once per spec FILE (fixtures.ts), so the line
      // the first test rang up is still there: assert the count doesn't
      // CHANGE, not that it's zero.
      const linesBefore = await page.locator('.basket .line-name').count();
      await tileA.click({ button: 'right' });
      await expect(grid(page)).toHaveClass(/jiggle-mode/);
      await page.waitForTimeout(300);
      await expect(page.locator('.basket .line-name')).toHaveCount(linesBefore);

      await dragPast(tileA, tileB);
      await expect.poll(() => codesInOrder(page, RUN2)).toEqual([ITEM_B.barcode, ITEM_A.barcode]);

      // A pointerdown on the basket panel — outside #buttons-grid — exits
      // the mode and persists.
      const reorderResponse = page.waitForResponse(
        (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
      );
      const basket = (await page.locator('.basket').boundingBox())!;
      await page.mouse.click(basket.x + basket.width / 2, basket.y + 10);
      expect((await reorderResponse).status()).toBe(204);
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      await page.reload();
      await expect.poll(() => codesInOrder(page, RUN2)).toEqual([ITEM_B.barcode, ITEM_A.barcode]);

      assertClean();
    } finally {
      await cleanupItems(page, [ITEM_A, ITEM_B]);
    }
  });

  // ut-docs#2402 independent-review finding: app.js's inAllGrid() guard
  // (ut-docs#2294 fallout) keeps a long-press on an All-grid tile from
  // arming jiggle mode. ut-docs#2613 retired the strip's All tab, so the
  // only All grid left is the all_filter_chips browsing mode's own
  // #buttons-grid-all -- this test now long-presses there. The seeded item
  // renders in it with the SAME data-code the shortcut carries, since
  // seedItems gives the shortcut the item's own barcode.
  test('a long-press on the all_filter_chips All grid never arms jiggle mode (ut-docs#2402)', async ({ page }) => {
    const assertClean = watchConsole(page);
    const { A: ITEM_A } = fixture('3');
    await seedItems(page, [ITEM_A]);
    await setBrowsingMode(page, 'all_filter_chips');
    try {
      await page.goto('/');
      await expect(page.locator('#browsing-category-chips')).toBeVisible();
      const allTile = page.locator(`#buttons-grid-all .btn-tile[data-code="${ITEM_A.barcode}"]`);
      await expect(allTile).toBeVisible();
      // The All grid lists every active catalog item (demo-seeded ones
      // included), so this fixture's own tile can render below the fold --
      // longPress() drives raw page.mouse coordinates (unlike .click(),
      // which auto-scrolls), so it needs the tile actually in the viewport.
      await allTile.scrollIntoViewIfNeeded();

      await longPress(allTile);
      await page.waitForTimeout(300);
      await expect(grid(page)).not.toHaveClass(/jiggle-mode/);
      // Badges exist in the DOM for every tile at all times (hidden via
      // .jiggle-mode, same as the rest of this file's "at rest" checks) --
      // this one's the tile actually long-pressed, so it's the one whose
      // badge would have shown if the guard were missing.
      await expect(allTile.locator('xpath=..').locator('[data-testid="tile-badge-edit"]')).toBeHidden();

      assertClean();
    } finally {
      // Every worker till boots in strip_overflow (worker-till.ts); put it
      // back so a later spec file on this worker isn't left in chip mode.
      await setBrowsingMode(page, 'strip_overflow');
      await cleanupItems(page, [ITEM_A]);
    }
  });
});
