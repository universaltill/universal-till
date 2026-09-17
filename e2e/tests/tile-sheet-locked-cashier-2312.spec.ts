import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { ensureOperator, ADMIN_PIN } from './helpers';

// ut-docs#2312: the sell-screen tile long-press sheet (#2285) now shows
// Move/Remove locked (a lock icon, muted styling — never the real
// `disabled` attribute) for an operator who lacks catalog_management,
// rather than hiding them outright ("show, don't hide" — the UX decision
// recorded on #2285). Tapping a locked action must still reach the server
// and land on the real manager-PIN elevation prompt (checkOrElevate,
// elevation.go), never a silent no-op.
//
// This needs the `auth` project — a real cashier-vs-manager session — the
// same reasoning session-expiry-redirect-admin-2157.spec.ts documents for
// itself. It creates a second (cashier) operator on top of the wizard-
// created admin ensureOperator() logs in as, which no other spec in this
// suite does yet, so there is no existing "create + PIN-login as a second
// role" helper to reuse — written out here instead of assumed to exist.
//
// Run for real (orchestrating cycle, ut-docs#2312 review pass): a
// pre-installed-Chromium sandbox with npm ci'd e2e/node_modules DOES exist
// here, and this spec passes against the `auth` project — 1/1, including
// the full checkOrElevate round trip (real cashier session, real tap,
// real PIN-approval modal, real elevation-consumed mutation). The Dev's
// original version failed on first run for an unrelated reason: seedOneTile
// added only one tile, so Move-later's pre-existing `HasNext`-false
// `disabled` (an edge-of-list state, unrelated to .Locked) masked the gate
// this spec actually exercises — fixed by seeding a second (filler) tile
// so the first one has somewhere to move to.
//
// Not added to guard-e2e-fixtures-import.sh's EXEMPT_FILES despite sharing
// AUTH_ONLY_SPECS membership with all five specs that ARE exempt: this one
// imports `test`/`expect` from ./fixtures correctly (the guard's actual
// requirement) and resetPosOncePerFile's session-less first request here
// just redirects to /login (303) rather than throwing, so nothing leaks —
// verified by running it, not inferred. The Go-side gate itself
// (checkOrElevate on every route this sheet posts to) is also proven by
// internal/pages/buttons_api_catalog_management_gate_test.go's
// TestTileSheet_LockedForCashierGrantedForManager, which asserts the same
// HTML markers (the `locked`-class absence of `disabled`, the
// `tile-sheet-lock` icon) this spec drives through a real browser.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier2312${RUN}`;
const CASHIER_PIN = '135790';
const ITEM = { name: `Sheet2312 Item ${RUN}`, sku: `SHEET2312${RUN}` };
// A second tile purely so the first has somewhere to move to — Move-later
// carries its own unrelated `disabled` when `.HasNext` is false (last tile
// in the grid), which would otherwise be indistinguishable from the Locked
// gate this spec actually exercises.
const FILLER_ITEM = { name: `Sheet2312 Filler ${RUN}`, sku: `SHEET2312F${RUN}` };

// A real long-press: pointerdown, hold past app.js's 500ms threshold, then
// release without moving — the sheet-opening path. Copied from
// sell-tile-long-press-2285.spec.ts's own identical helper (that file has
// no exported symbols to import from).
async function longPress(tile: Locator) {
  const page = tile.page();
  const box = (await tile.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

// Creates one active catalog item and adds it as a sell-screen quick
// button, while the ADMIN session ensureOperator() left active is still
// the request's cookie — mirrors sell-tile-long-press-2285.spec.ts's own
// seedItems, simplified to the one tile this spec needs.
async function seedOneTile(page: Page): Promise<void> {
  for (const item of [ITEM, FILLER_ITEM]) {
    const createResp = await page.request.post('/api/catalog/item', {
      form: { name: item.name, price: '150', sku: item.sku },
    });
    expect(createResp.ok(), 'create catalog item').toBe(true);

    await page.goto('/catalog');
    const row = page.locator(`.catalog-row[data-name="${item.name}"]`);
    const itemId = (await row.first().getAttribute('data-id'))!;

    const addResp = await page.request.post('/api/buttons/add', {
      form: { itemId, label: item.name, code: item.sku },
    });
    expect(addResp.ok(), 'add shortcut button').toBe(true);
  }
}

// Creates a cashier operator and gives them a PIN, still on the admin
// session — POST /api/users itself is checkOrElevate("user_management")-
// gated, but the admin ensureOperator() logged in as already has that
// action granted (migration 001_init.sql), so this is a plain, un-elevated
// create. The row's own PIN-set form (`hx-post="/api/users/{id}/pin"`,
// users.html) is where this reads the new user's id back from — there is
// no JSON response to parse it out of otherwise.
async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 2312', role: 'cashier' },
  });
  expect(createResp.ok(), 'create cashier user').toBe(true);

  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  expect(pinAction, 'cashier row has a pin-set form').not.toBeNull();
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];

  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginAsCashier(page: Page): Promise<void> {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of CASHIER_PIN.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
}

test.describe('Sell-screen tile sheet locked for a cashier lacking catalog_management (ut-docs#2312)', () => {
  test('cashier sees Move/Remove locked, never hidden; a tap opens the real manager-PIN prompt', async ({ page }) => {
    await ensureOperator(page); // admin — first-boot wizard or PIN re-login
    await seedOneTile(page);
    await createCashier(page);
    await loginAsCashier(page);

    await page.goto('/');
    const tile = page.locator(`.btn-tile[data-name="${ITEM.name}"]`);
    await expect(tile).toBeVisible();

    await longPress(tile);
    await expect(page.locator('#tile-sheet')).toBeVisible();

    // "Show, don't hide" (#2285's own decision): the locked actions are
    // still real, visible, ENABLED controls — only the `.locked` class and
    // a lock badge distinguish them from a granted operator's sheet. Real
    // `disabled` is reserved for IsReplica, which does not apply here.
    const moveLater = page.locator('[data-testid="tile-sheet-move-later"]');
    await expect(moveLater).toHaveClass(/locked/);
    await expect(moveLater).toBeEnabled();
    await expect(page.locator('#tile-sheet .tile-sheet-lock').first()).toBeVisible();

    const removeBtn = page.locator('[data-testid="tile-sheet-remove"]');
    await expect(removeBtn).toHaveClass(/locked/);
    await expect(removeBtn).toBeEnabled();

    // A tap reaches the server (checkOrElevate) and lands on the real
    // elevation prompt — never a silent no-op, and never the move
    // actually applying without a PIN.
    await moveLater.click();
    await expect(page.locator('#elevation-modal')).toBeVisible();
    await expect(page.locator('#elevation-modal')).toContainText(/PIN|elevation/i);

    // Approve with the ADMIN's own PIN — proves the full checkOrElevate
    // round trip (a granted approver's PIN actually lets the move through),
    // not just that some dialog opened.
    await page.locator('#elevation-modal input[name=override_pin]').fill(ADMIN_PIN);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/buttons/move') && r.request().method() === 'POST'),
      page.locator('#elevation-modal button[type=submit]').click(),
    ]);
    await expect(page.locator('#elevation-modal')).toBeHidden();
  });
});
