import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#901: /locations and /registers both 403'd permanently under
// UT_AUTH=off (this suite's default project) because their requireManager
// closures read auth.FromContext directly instead of going through
// canPerform(d, r, "settings") -- the escape hatch every other admin page
// already uses. That gap is also why neither page had any e2e coverage
// before this file: the default project (this one) is the only one that
// can drive them without a real login flow (see e2e/README.md), and they
// were unreachable here. Smoke-covers both pages' rename + activate/
// deactivate flows, which is what #898's own review had to fall back to a
// static CSS-harness screenshot for instead of a real e2e run.
//
// activeInput() below reads the toggle form's own hidden `active` field
// rather than the row's `.muted` text -- for /registers specifically, the
// location cell ALSO renders a `.muted` span ("None") whether the row is
// active or not, so a plain ".muted" presence check is vacuous there
// (independent review finding, ut-docs#901). The hidden field flips
// 0<->1 with .IsActive and exists on both pages, so this is one true,
// locale-independent assertion for both.

// The toggle form's own hidden `active` input -- see the file-header note.
function activeInput(row) {
  return row.locator('form.users-inline').nth(1).locator('input[name="active"]');
}

test.describe('/locations admin page (ut-docs#901)', () => {
  test('renders under UT_AUTH=off, and create/rename/deactivate/reactivate all work', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/locations');
    // The permanent-403 regression: a failed GET here renders a plain-text
    // 403 body (common.LocalizedError -> http.Error), which has no <h1> --
    // not the locations table.
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('table.table')).toBeVisible();

    // Create a fresh location so this test never touches the seeded
    // Main/Back/Warehouse rows (any of #901/#895's other e2e specs could be
    // sharing this same server -- workers: 1, but still one till).
    const name = `E2E Location ${Date.now()}`;
    await page.locator('.users-form input[name="name"]').fill(name);
    await page.locator('.users-form button[type="submit"]').click();
    await page.waitForURL((u) => u.pathname === '/locations');

    const row = page.locator('tbody tr', { hasText: name });
    await expect(row, 'newly created location must appear in the table').toBeVisible();

    // Rename.
    const renamed = `${name} Renamed`;
    await row.locator('input[name="name"].rename-input').fill(renamed);
    await row.locator('form.users-inline').first().locator('button[type="submit"]').click();
    await page.waitForURL((u) => u.pathname === '/locations');
    const renamedRow = page.locator('tbody tr', { hasText: renamed });
    await expect(renamedRow).toBeVisible();

    // Deactivate -- safe: the seeded Main/Back/Warehouse locations stay
    // active, so this is never the shop's last active location.
    await renamedRow.locator('form.users-inline').nth(1).locator('button[type="submit"]').click();
    await expect(activeInput(renamedRow)).toHaveValue('1'); // now offers "activate" -> currently inactive

    // Reactivate. Leaves this test's own new/renamed row behind, active --
    // no spec on this shared e2e server asserts a row/option count on
    // /locations, so that's harmless, just not a full restore to the
    // state this test found (independent review finding, ut-docs#901).
    await renamedRow.locator('form.users-inline').nth(1).locator('button[type="submit"]').click();
    await expect(activeInput(renamedRow)).toHaveValue('0'); // now offers "deactivate" -> currently active

    assertClean();
  });
});

test.describe('/registers admin page (ut-docs#901)', () => {
  test('renders under UT_AUTH=off, and create/rename/deactivate/reactivate all work', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/registers');
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('table.table')).toBeVisible();

    // Create TWO fresh registers so deactivating one never trips the
    // "shop must keep one active register" guard, regardless of how many
    // registers this shared e2e till already happens to have.
    const nameA = `E2E Register A ${Date.now()}`;
    const nameB = `E2E Register B ${Date.now()}`;
    for (const n of [nameA, nameB]) {
      await page.locator('.users-form input[name="name"]').fill(n);
      await page.locator('.users-form button[type="submit"]').click();
      await page.waitForURL((u) => u.pathname === '/registers');
    }

    const rowA = page.locator('tbody tr', { hasText: nameA });
    await expect(rowA, 'newly created register A must appear in the table').toBeVisible();

    // Rename.
    const renamedA = `${nameA} Renamed`;
    await rowA.locator('input[name="name"].rename-input').fill(renamedA);
    await rowA.locator('form.users-inline').first().locator('button[type="submit"]').click();
    await page.waitForURL((u) => u.pathname === '/registers');
    const renamedRowA = page.locator('tbody tr', { hasText: renamedA });
    await expect(renamedRowA).toBeVisible();

    // Deactivate A -- register B (still active) keeps this from tripping
    // the last-active-register guard.
    await renamedRowA.locator('form.users-inline').nth(1).locator('button[type="submit"]').click();
    await expect(activeInput(renamedRowA)).toHaveValue('1'); // now offers "activate" -> currently inactive

    // Reactivate. Leaves both this test's new/renamed rows behind, active --
    // same server-state note as the locations spec above.
    await renamedRowA.locator('form.users-inline').nth(1).locator('button[type="submit"]').click();
    await expect(activeInput(renamedRowA)).toHaveValue('0'); // now offers "deactivate" -> currently active

    assertClean();
  });
});
