import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1221: Till Designer's tile reordering used HTML5 drag-and-drop
// (draggable="true" + dragstart/dragover/drop), which WebKitGTK (and
// browsers generally) never synthesizes from touch input — so on the till's
// actual touchscreen the whole reorder feature was dead; it only ever
// worked with a plugged-in mouse. Replaced with explicit move-up/move-down
// buttons per tile (`web/ui/partials/buttons_admin.html`): one `<button>`
// activation is a single mechanism that already works by tap, click AND
// keyboard, so there is no touch-specific gesture-detection code to get
// wrong the way a Pointer Events drag would still need.
//
// HONESTY NOTE (per the `ux` skill's rule on touch-sensitive changes): this
// is verified via Playwright's synthetic `touchscreen`/`hasTouch` context,
// not real touch hardware. Unlike a drag gesture, though, a `<button>`
// activation is not a synthesized multi-event gesture the browser has to
// recognize — touchend firing `click` on a button is basic, unconditional
// browser behavior — so this is a materially stronger claim than "verified
// with a mouse" would be for the drag-based approach it replaces.

test.describe('Designer tile reorder (ut-docs#1221)', () => {
  test('move-down/move-up buttons reorder tiles and persist the new order', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/designer');

    const grid = page.locator('#buttons-grid-admin');
    const tileCount = await grid.locator('.reorderable-tile').count();
    expect(tileCount, 'designer needs at least two demo shortcut tiles to test reordering').toBeGreaterThan(1);

    const first = grid.locator('.reorderable-tile').nth(0);
    const second = grid.locator('.reorderable-tile').nth(1);
    const firstCode = await first.getAttribute('data-code');
    const secondCode = await second.getAttribute('data-code');

    const reorderResponse = page.waitForResponse(
      (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
    );
    await first.locator('.move-down').click();
    const res = await reorderResponse;
    expect(res.ok()).toBe(true);

    // The two tiles swapped: what was first is now second, and vice versa.
    await expect(grid.locator('.reorderable-tile').nth(0)).toHaveAttribute('data-code', secondCode!);
    await expect(grid.locator('.reorderable-tile').nth(1)).toHaveAttribute('data-code', firstCode!);

    // Reload and confirm the swap actually persisted server-side, not just
    // client-side DOM state.
    await page.reload();
    await expect(grid.locator('.reorderable-tile').nth(0)).toHaveAttribute('data-code', secondCode!);
    await expect(grid.locator('.reorderable-tile').nth(1)).toHaveAttribute('data-code', firstCode!);

    // Move it back with move-up so this test leaves shared demo state as it
    // found it (the dev till server persists reorders across runs).
    const restoreResponse = page.waitForResponse(
      (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
    );
    await grid.locator('.reorderable-tile').nth(1).locator('.move-up').click();
    await restoreResponse;
    await expect(grid.locator('.reorderable-tile').nth(0)).toHaveAttribute('data-code', firstCode!);

    assertClean();
  });

  test('first tile has no move-up, last tile has no move-down', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/designer');

    const grid = page.locator('#buttons-grid-admin');
    const tiles = grid.locator('.reorderable-tile');
    const count = await tiles.count();
    expect(count).toBeGreaterThan(1);

    await expect(tiles.nth(0).locator('.move-up')).toBeDisabled();
    await expect(tiles.nth(0).locator('.move-down')).toBeEnabled();
    await expect(tiles.nth(count - 1).locator('.move-down')).toBeDisabled();
    await expect(tiles.nth(count - 1).locator('.move-up')).toBeEnabled();

    assertClean();
  });

  // The actual regression: prove the fix works when driven by touch, not
  // just a mouse click — the previous drag-based implementation could not
  // do this at all (see HONESTY NOTE above for what this test can and
  // cannot prove about real touch hardware).
  test('move-down button reorders tiles from a touch context (ut-docs#1221)', async ({ browser }) => {
    const ctx = await browser.newContext({ hasTouch: true });
    const page = await ctx.newPage();
    const assertClean = watchConsole(page);

    await page.goto('/designer');
    const grid = page.locator('#buttons-grid-admin');
    const tileCount = await grid.locator('.reorderable-tile').count();
    expect(tileCount).toBeGreaterThan(1);

    const first = grid.locator('.reorderable-tile').nth(0);
    const secondCode = await grid.locator('.reorderable-tile').nth(1).getAttribute('data-code');
    const firstCode = await first.getAttribute('data-code');

    const reorderResponse = page.waitForResponse(
      (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
    );
    await first.locator('.move-down').tap();
    await reorderResponse;

    await expect(grid.locator('.reorderable-tile').nth(0)).toHaveAttribute('data-code', secondCode!);
    await expect(grid.locator('.reorderable-tile').nth(1)).toHaveAttribute('data-code', firstCode!);

    // Restore original order (shared dev till server persists state).
    const restoreResponse = page.waitForResponse(
      (r) => r.url().includes('/api/buttons/reorder') && r.request().method() === 'POST',
    );
    await grid.locator('.reorderable-tile').nth(1).locator('.move-up').tap();
    await restoreResponse;

    assertClean();
    await ctx.close();
  });

  // Review finding (2026-08-30): a fast multi-step move used to fire one
  // fire-and-forget POST per click, each carrying a full order snapshot —
  // with no serialization, a slow response for an EARLIER click could land
  // AFTER a later click's response and overwrite it with a stale order.
  // Delaying the first request reproduces exactly that race unless the
  // sends are chained.
  test('rapid repeated moves persist the final order, not a stale one (ut-docs#1221)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/designer');

    const grid = page.locator('#buttons-grid-admin');
    const tileCount = await grid.locator('.reorderable-tile').count();
    expect(tileCount, 'needs at least 3 tiles to move one two places').toBeGreaterThan(2);

    const startCode = await grid.locator('.reorderable-tile').nth(0).getAttribute('data-code');

    let requestNumber = 0;
    await page.route('**/api/buttons/reorder', async (route) => {
      requestNumber++;
      // Delay only the FIRST request substantially -- the one most likely to
      // be overtaken by a later, faster request if sends aren't serialized.
      if (requestNumber === 1) {
        await new Promise((r) => setTimeout(r, 600));
      }
      await route.continue();
    });

    // Two rapid clicks, back to back, with no wait between them -- exactly
    // the "repeated tap/Enter keeps stepping it further" gesture the focus-
    // retention behavior is built around. Locate the moving tile by its
    // stable data-code, not by position (`nth(0)`) -- insertBefore moves the
    // real DOM node rather than recreating it, so a code-based locator keeps
    // tracking the SAME tile across both clicks, the way a finger tapping
    // the same physical button twice would.
    const movingTile = grid.locator(`.reorderable-tile[data-code="${startCode}"]`);
    await movingTile.locator('.move-down').click();
    await movingTile.locator('.move-down').click();

    // Both requests this triggered must resolve before checking persistence.
    await expect
      .poll(() => requestNumber, { message: 'expected two reorder requests', timeout: 5000 })
      .toBeGreaterThanOrEqual(2);
    // The slow first request is in flight for 600ms; give it (and the
    // second, chained behind it) time to actually complete.
    await page.waitForTimeout(1000);

    const expectedCode = await grid.locator('.reorderable-tile').nth(2).getAttribute('data-code');
    expect(expectedCode).toBe(startCode);

    // Reload with no route interception and confirm the SERVER agrees with
    // the DOM — this is what finding #1 got wrong: the DOM was right, the
    // persisted order was not.
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    await page.reload();
    await expect(grid.locator('.reorderable-tile').nth(2)).toHaveAttribute('data-code', startCode!);

    // Restore original order (shared dev till server persists state).
    const restoreResponse1 = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder'));
    await grid.locator(`[data-code="${startCode}"]`).locator('.move-up').click();
    await restoreResponse1;
    const restoreResponse2 = page.waitForResponse((r) => r.url().includes('/api/buttons/reorder'));
    await grid.locator(`[data-code="${startCode}"]`).locator('.move-up').click();
    await restoreResponse2;
    await expect(grid.locator('.reorderable-tile').nth(0)).toHaveAttribute('data-code', startCode!);

    assertClean();
  });
});
