import { test, expect } from './fixtures';
import type { Locator, Page } from '@playwright/test';
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
// ut-docs#2124: /locations moved off the inline .users-form/.users-inline
// pattern onto the shared record_dialog/list_header standard (ut-docs#2010)
// -- its interactions below updated to match (tap the list-header "+" to
// create, tap a row to open it prefilled, Save/the destructive form inside
// the dialog), same DOM this suite's own categories-record-dialog-2010.spec.ts
// and locations-record-dialog-2124.spec.ts already drive.
//
// /registers is NOT yet converted on THIS branch (that's ut-docs#2185,
// reviewed separately) -- its describe block below is untouched, still the
// pre-#2010 inline-form interaction. Once #2185 merges to main, update this
// file's /registers block the same way (see that PR's own equivalent fix to
// this same file) rather than leaving it stale.
//
// activeInput() below (the /registers block only) reads the toggle form's
// own hidden `active` field rather than the row's `.muted` text -- for
// /registers specifically the location cell ALSO renders a `.muted` span
// ("None") whether the row is active or not, so a plain ".muted" presence
// check is vacuous there (independent review finding, ut-docs#901). The
// /locations block below uses the new record_dialog markup's own
// data-field-active attribute instead, the equivalent one-true-signal for
// the new pattern.

// The toggle form's own hidden `active` input -- see the file-header note.
function activeInput(row: Locator) {
  return row.locator('form.users-inline').nth(1).locator('input[name="active"]');
}

// Observed real, if infrequent, flakiness driving this exact server (a
// throwaway till boots fresh per e2e run, and record-dialog.js's click
// delegation is registered at script-load time on the page HX-Redirect
// navigates to): a row click landing right after that navigation
// occasionally has no effect the first time. A row click is otherwise a
// simple action with nothing to legitimately retry on real content
// grounds -- this exists purely to absorb that timing gap, bounded and
// visible in the loop count rather than silently retried forever.
async function openRowDialog(page: Page, row: Locator, dialogSel: string) {
  const dialog = page.locator(dialogSel);
  for (let attempt = 0; attempt < 3; attempt++) {
    await row.click();
    try {
      await expect(dialog).toBeVisible({ timeout: 2000 });
      return;
    } catch {
      if (attempt === 2) throw new Error(`${dialogSel} did not open after 3 row-click attempts`);
    }
  }
}

test.describe('/locations admin page (ut-docs#901)', () => {
  test('renders under UT_AUTH=off, and create/rename/deactivate/reactivate all work', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/locations');
    // The permanent-403 regression: a failed GET here renders a plain-text
    // 403 body (common.LocalizedError -> http.Error), which has no <h1> --
    // not the locations table. (Unlike /registers, this e2e server's own
    // fresh boot DOES seed a stock_locations row via 001_init.sql, so the
    // table always has content and the visibility check below is safe as
    // the very first assertion, no create-first ordering needed.)
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('table.table')).toBeVisible();

    // Create a fresh location so this test never touches the seeded
    // Main/Back/Warehouse rows (any of #901/#895's other e2e specs could be
    // sharing this same server -- workers: 1, but still one till).
    const name = `E2E Location ${Date.now()}`;
    await page.locator('#locations-new').click();
    await expect(page.locator('#location-dialog')).toBeVisible();
    await page.locator('#location-form input[name="name"]').fill(name);
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/locations'),
      page.locator('#location-dialog .record-dialog-save').click(),
    ]);
    // HX-Redirect drives a real browser navigation (redirectLocations's own
    // doc comment) -- waitForURL can resolve as soon as the new URL commits,
    // before the fresh document has actually finished loading, so a click
    // right after can land before record-dialog.js's own click delegation
    // (registered at script-load time on the new page) is live. Explicit
    // load-state wait, same fix this file needed for real in CI (ut-docs#2124).
    await page.waitForLoadState('load');

    const row = page.locator('#locations-table .location-row', { hasText: name });
    await expect(row, 'newly created location must appear in the table').toBeVisible();

    // Rename.
    const renamed = `${name} Renamed`;
    await openRowDialog(page, row, '#location-dialog');
    await page.locator('#location-form input[name="name"]').fill(renamed);
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/locations'),
      page.locator('#location-dialog .record-dialog-save').click(),
    ]);
    await page.waitForLoadState('load');
    const renamedRow = page.locator('#locations-table .location-row', { hasText: renamed });
    await expect(renamedRow).toBeVisible();

    // Deactivate -- safe: the seeded Main/Back/Warehouse locations stay
    // active, so this is never the shop's last active location. Behind
    // hx-confirm (a real browser confirm()) -- Playwright auto-dismisses
    // an unhandled one, which would silently no-op the click, so accept
    // it explicitly.
    await openRowDialog(page, renamedRow, '#location-dialog');
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/locations'),
      page.locator('#location-dialog form[data-record-when="active=1"] button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('load');
    await expect(renamedRow).toHaveAttribute('data-field-active', '0'); // now offers "activate" -> currently inactive

    // Reactivate. Leaves this test's own new/renamed row behind, active --
    // no spec on this shared e2e server asserts a row/option count on
    // /locations, so that's harmless, just not a full restore to the
    // state this test found (independent review finding, ut-docs#901).
    await openRowDialog(page, renamedRow, '#location-dialog');
    await Promise.all([
      page.waitForURL((u) => u.pathname === '/locations'),
      page.locator('#location-dialog form[data-record-when="active=0"] button[type="submit"]').click(),
    ]);
    await expect(renamedRow).toHaveAttribute('data-field-active', '1'); // now offers "deactivate" -> currently active

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
