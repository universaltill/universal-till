import { test, expect } from '@playwright/test';
import { watchConsole, ensureOperator } from './helpers';

// GET /tables (internal/pages/tables_page.go) used to requireManager-gate
// every route this spec drives with no UT_AUTH=off bypass, so the default
// (auth-off) till 403'd all of it and this spec had to run on the AUTH
// project instead. Fixed by ut-docs#902 (following #901's precedent for
// locations/registers): /tables now goes through canPerform(d, r,
// "settings"), which has the UT_AUTH=off escape hatch every other admin
// page already uses -- this spec runs on the default project now
// (playwright.config.ts's AUTH_ONLY_SPECS no longer lists it).
// ensureOperator() below is a no-op under UT_AUTH=off (neither the /setup
// nor /login branch triggers), kept only so deactivateAllTables() stays
// identical to before this fix.

// ut-docs#826: follow-up from the 2026-08-19 independent review of #814
// (Table floor plan, ADR-0054) -- the floor-plan editor's SVG table tiles
// were repositionable by pointer-drag only, so a keyboard-only or
// switch-access operator could create/edit a table's name/area/seats/shape
// (the plain HTML form) but had no way at all to place it on the map.
//
// Fix: every `.table-node` gets `tabindex="0"` while editing (mirroring the
// same `window.tablesEditing` gate pointer-drag already uses), a
// `:focus-visible` outline (the ut-docs#797 --focus-border token, not raw
// --accent, so it clears WCAG 1.4.11 in every theme), and an arrow-key nudge
// (Shift for a bigger step) that persists through the exact same
// clamp()+POST /api/tables/{id}/position path drag-end already used.
//
// This drives a REAL focused element via real Tab/arrow keydowns (not a
// scripted .focus() call, which would never trigger :focus-visible under
// Chromium's own heuristic) against the real served page -- same standard
// as tender-panel-reachable.spec.ts's real-click precedent.

// Both tests assume theirs is the ONLY active table on the plan (so
// `.table-node` / Tab-from-toggle unambiguously mean "the one this test just
// created") -- deactivating whatever an earlier test in this file (or a
// re-run against a reused server, see playwright.config.ts's
// reuseExistingServer) left behind makes that true regardless of run order,
// rather than relying on the two tests' labels happening to sort a
// particular way.
async function deactivateAllTables(page) {
  await ensureOperator(page); // fresh Playwright context per test -> log in each time
  await page.goto('/tables');
  for (;;) {
    const btn = page.locator('form[action$="/active"] button', { hasText: 'Deactivate' }).first();
    if ((await btn.count()) === 0) break;
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), btn.click()]);
  }
}

async function createTable(page, label: string) {
  // Scoped to the bottom-of-page card: since ut-docs#1025 the tap-to-add
  // dialog is a second form[action="/api/tables"], so the bare selector
  // would be a strict-mode violation.
  await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill(label);
  await Promise.all([
    page.waitForURL((u) => u.pathname === '/tables'),
    page.locator('.users-form form[action="/api/tables"] button[type=submit]').click(),
  ]);
}

async function addTable(page, label: string) {
  await deactivateAllTables(page);
  await createTable(page, label);
}

test.describe('Tables floor plan: keyboard reposition (ut-docs#826)', () => {
  test('a table can be selected and moved with only the keyboard, and persists', async ({ page }) => {
    const assertClean = watchConsole(page);
    await addTable(page, 'E2E Kbd Table 826');

    // The new table lands at the canvas centre (internal/pages/tables_page.go)
    // -- confirm it made the floor plan before touching the editor.
    const node = page.locator('.table-node').first();
    await expect(node).toBeVisible();
    await expect(page.locator('.table-node')).toHaveCount(1);
    const startX = Number(await node.getAttribute('data-x'));
    const startY = Number(await node.getAttribute('data-y'));
    expect(startX).toBe(500);
    expect(startY).toBe(500);

    await page.locator('#tables-edit-toggle').click();

    // Real keyboard traversal, not node.focus() -- only a genuine keyboard
    // focus is guaranteed to satisfy :focus-visible's heuristic.
    await page.keyboard.press('Tab');
    const isNodeFocused = await node.evaluate((el) => el === document.activeElement);
    expect(isNodeFocused, 'Tab from the edit toggle must land on the table tile').toBe(true);

    // Visible focus indicator (acceptance criterion): a real outline, not
    // "outline: none" left over from nothing having set it.
    const outlineStyle = await node.evaluate((el) => getComputedStyle(el).outlineStyle);
    expect(outlineStyle).not.toBe('none');

    // A plain arrow nudge moves by the small step...
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => node.getAttribute('data-x')).toBe(String(startX + 10));
    expect(await node.getAttribute('data-y')).toBe(String(startY));

    // ...Shift+arrow moves by the larger step, and the move is visible
    // immediately (before the debounced save below has even fired).
    await page.keyboard.down('Shift');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.up('Shift');
    await expect.poll(() => node.getAttribute('data-y')).toBe(String(startY + 40));

    const expectedX = startX + 10;
    const expectedY = startY + 40;

    // The save is debounced (~400ms) -- wait for the real POST rather than a
    // fixed sleep, then confirm it's the position endpoint for this table.
    const req = await page.waitForRequest(
      (r) => r.method() === 'POST' && r.url().includes('/position'),
      { timeout: 2000 },
    );
    expect(req.postData()).toContain(`pos_x=${expectedX}`);
    expect(req.postData()).toContain(`pos_y=${expectedY}`);

    // Reload with editing off, and confirm the server actually persisted it
    // -- not just the in-memory dataset the keydown handler wrote locally.
    await page.reload();
    const reloaded = page.locator('.table-node').first();
    await expect(reloaded).toBeVisible();
    expect(await reloaded.getAttribute('data-x')).toBe(String(expectedX));
    expect(await reloaded.getAttribute('data-y')).toBe(String(expectedY));

    // Tab order regression: outside edit mode the tile must NOT be
    // keyboard-reachable (tabIndex mirrors window.tablesEditing).
    expect(await reloaded.getAttribute('tabindex')).toBe('-1');

    assertClean();
  });

  test('a nudge made just before leaving edit mode is not dropped (debounce flush)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await addTable(page, 'E2E Kbd Flush 826');

    const node = page.locator('.table-node').first();
    await expect(node).toBeVisible();
    await expect(page.locator('.table-node')).toHaveCount(1);
    const startX = Number(await node.getAttribute('data-x'));

    await page.locator('#tables-edit-toggle').click();
    await page.keyboard.press('Tab');
    await page.keyboard.press('ArrowRight');

    // Leave edit mode IMMEDIATELY -- well inside the ~400ms debounce window
    // -- and confirm the pending move is flushed rather than lost.
    const [req] = await Promise.all([
      page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('/position'), { timeout: 2000 }),
      page.locator('#tables-edit-toggle').click(),
    ]);
    expect(req.postData()).toContain(`pos_x=${startX + 10}`);

    assertClean();
  });

  // Independent review of the first draft (ut-docs#826) reproduced this
  // live: pendingSave was a single global slot keyed by nothing, so nudging
  // table A (scheduling its debounced save), then Tabbing to table B and
  // nudging it BEFORE A's ~400ms debounce fired, silently discarded A's
  // move -- clearTimeout+replace clobbered A's pending save with B's, with
  // no error surfaced (the request for A's move simply never happened).
  // Fixed by keying the debounce map by node. This is exactly the workflow
  // a keyboard operator arranging several tables would actually do.
  test('nudging table A then tabbing to table B before A\'s debounce fires drops neither move', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTable(page, 'E2E Kbd Race A 826');
    await createTable(page, 'E2E Kbd Race B 826');

    await expect(page.locator('.table-node')).toHaveCount(2);
    // ListTablesWithState orders by area_zone, label -- both tables share an
    // empty area_zone, so "Race A" sorts (and tabs) before "Race B".
    const nodeA = page.locator('.table-node').nth(0);
    const nodeB = page.locator('.table-node').nth(1);
    const aStartX = Number(await nodeA.getAttribute('data-x'));
    const bStartX = Number(await nodeB.getAttribute('data-x'));

    const positionRequests: string[] = [];
    page.on('request', (r) => {
      if (r.method() === 'POST' && r.url().includes('/position')) positionRequests.push(r.postData() ?? '');
    });

    await page.locator('#tables-edit-toggle').click();
    await page.keyboard.press('Tab'); // -> table A
    await page.keyboard.press('ArrowRight'); // schedules A's save
    await page.keyboard.press('Tab'); // -> table B, well inside A's debounce window
    await page.keyboard.press('ArrowRight'); // schedules B's save

    // Both debounced saves must land -- neither node's pending save may
    // clobber the other's.
    await expect.poll(() => positionRequests.length, { timeout: 2000 }).toBeGreaterThanOrEqual(2);
    expect(positionRequests.some((d) => d.includes(`pos_x=${aStartX + 10}`)), 'table A\'s move must not be dropped').toBe(true);
    expect(positionRequests.some((d) => d.includes(`pos_x=${bStartX + 10}`)), 'table B\'s move must not be dropped').toBe(true);

    // And it's actually persisted, not just requested.
    await page.reload();
    const reloaded = page.locator('.table-node');
    await expect(reloaded).toHaveCount(2);
    expect(await reloaded.nth(0).getAttribute('data-x')).toBe(String(aStartX + 10));
    expect(await reloaded.nth(1).getAttribute('data-x')).toBe(String(bStartX + 10));

    assertClean();
  });
});
