import { test, expect } from '@playwright/test';
import { watchConsole, ensureOperator } from './helpers';

// ut-docs#1025: two bounded floor-plan editor fixes.
//
// 1. Tap-to-place: in edit mode, tapping/clicking empty canvas (the
//    .floorplan-bg rect) opens an add-table dialog with the tapped position
//    pre-filled, and the plain-form POST creates the table THERE instead of
//    at the canvas centre. The bottom-of-page add form (no pos fields) still
//    lands at the centre — covered by the Go handler test
//    (internal/pages/tables_page_test.go, TestTablesPageCreate_TapToPlacePosition).
//
// 2. `touch-action: none` on the plan's SVG is now gated on
//    `#floorplan-section.editing` instead of unconditional, so the live view
//    no longer swallows native touch panning — this page's contribution to
//    the cross-page touch-scroll report ut-docs#1021. HONESTY NOTE: the CSS
//    test below proves the SCOPING of the rule (computed touch-action flips
//    with the .editing class), NOT that real touch hardware actually scrolls
//    the page — that hardware verification stays tracked under ut-docs#1021
//    and is out of scope here.

// Same single-active-table hygiene as tables-keyboard-reposition-826.spec.ts:
// deactivate whatever earlier tests left behind so `.table-node` counts are
// deterministic regardless of run order / a reused server.
async function deactivateAllTables(page) {
  await ensureOperator(page); // fresh Playwright context per test -> log in each time
  await page.goto('/tables');
  for (;;) {
    const btn = page.locator('form[action$="/active"] button', { hasText: 'Deactivate' }).first();
    if ((await btn.count()) === 0) break;
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), btn.click()]);
  }
}

// The bottom-of-page card form — the pre-#1025 add path, unchanged.
async function createTableViaCard(page, label: string) {
  await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill(label);
  await Promise.all([
    page.waitForURL((u) => u.pathname === '/tables'),
    page.locator('.users-form form[action="/api/tables"] button[type=submit]').click(),
  ]);
}

test.describe('Tables floor plan: tap-to-add + touch-action scoping (ut-docs#1025)', () => {
  test('touch-action on the plan SVG is none only while editing (CSS scoping, not hardware scroll)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    // The floor-plan card only renders when at least one table exists.
    await createTableViaCard(page, 'E2E Touch 1025');

    const svg = page.locator('#floorplan');
    await expect(svg).toBeVisible();

    // Live view: native panning must NOT be disabled.
    expect(await svg.evaluate((el) => getComputedStyle(el).touchAction)).not.toBe('none');

    // Edit mode: the drag surface disables native panning again.
    await page.locator('#tables-edit-toggle').click();
    expect(await svg.evaluate((el) => getComputedStyle(el).touchAction)).toBe('none');

    // And back off when editing ends.
    await page.locator('#tables-edit-toggle').click();
    expect(await svg.evaluate((el) => getComputedStyle(el).touchAction)).not.toBe('none');

    assertClean();
  });

  test('clicking empty canvas outside edit mode does nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTableViaCard(page, 'E2E NoEdit 1025');

    await expect(page.locator('#floorplan')).toBeVisible();
    await page.locator('.floorplan-bg').click({ position: { x: 30, y: 30 } });
    await expect(page.locator('#table-add-modal')).not.toHaveAttribute('open', '');

    assertClean();
  });

  test('in edit mode, tapping empty canvas opens the dialog and creates the table at the tapped spot', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTableViaCard(page, 'E2E Seed 1025');
    await expect(page.locator('.table-node')).toHaveCount(1);

    await page.locator('#tables-edit-toggle').click();

    // Click near the top-left of the plan — far from the centre-placed seed
    // table, and far from the canvas centre (500,500) so "used the tapped
    // position" is distinguishable from the old centre fallback.
    const bg = page.locator('.floorplan-bg');
    const box = await bg.boundingBox();
    await bg.click({ position: { x: box!.width * 0.15, y: box!.height * 0.2 } });

    const dialog = page.locator('#table-add-modal');
    await expect(dialog).toHaveAttribute('open', '');

    // The click handler wrote the clamped canvas coordinates into the hidden
    // inputs before opening — read them back rather than re-deriving the
    // viewport->viewBox transform here, then assert the server persisted
    // exactly those (an end-to-end check of hidden inputs -> POST -> DB ->
    // re-render).
    const posX = await page.locator('#table-add-x').inputValue();
    const posY = await page.locator('#table-add-y').inputValue();
    expect(Number(posX)).toBeGreaterThanOrEqual(65); // TableEdgeInset clamp floor
    expect(Number(posX)).toBeLessThan(400); // clearly not the 500 centre fallback
    expect(Number(posY)).toBeLessThan(400);

    // The label input got focus (kiosk operators type immediately).
    await expect(page.locator('#table-add-label')).toBeFocused();

    await page.locator('#table-add-label').fill('E2E Tapped 1025');
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/tables'),
      dialog.locator('button[type=submit]').click(),
    ]);

    const created = page.locator(`.table-node:has-text("E2E Tapped 1025")`);
    await expect(created).toBeVisible();
    expect(await created.getAttribute('data-x')).toBe(posX);
    expect(await created.getAttribute('data-y')).toBe(posY);

    assertClean();
  });

  test('cancelling the dialog closes it and creates nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTableViaCard(page, 'E2E Cancel 1025');
    await expect(page.locator('.table-node')).toHaveCount(1);

    await page.locator('#tables-edit-toggle').click();
    await page.locator('.floorplan-bg').click({ position: { x: 40, y: 40 } });
    const dialog = page.locator('#table-add-modal');
    await expect(dialog).toHaveAttribute('open', '');

    await dialog.locator('button.secondary').click();
    await expect(dialog).not.toHaveAttribute('open', '');
    await expect(page.locator('.table-node')).toHaveCount(1);

    assertClean();
  });
});
