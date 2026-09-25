import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { ensureOperator, ADMIN_PIN, setBrowsingMode } from './helpers';

// ut-docs#2312: the sell-screen quick-button grid's jiggle edit mode
// (ut-docs#2339, replacing the retired #2285 long-press sheet) exposes a
// remove badge and a keyboard/drag reorder for EVERY signed-in operator,
// with no client-side permission check at all -- the mode is a pure class
// toggle. The actual protection is server-side: POST /api/buttons/remove
// and POST /api/buttons/reorder both gate on catalog_management
// (checkOrElevate), so a cashier lacking that grant must hit a real
// manager-PIN prompt before either one actually applies, never a silent
// no-op and never a silent (wrongly-treated-as-success) failure.
//
// This spec exists because merging #2312 with the just-landed #2339
// surfaced a real integration bug, found and fixed in the same session:
// utTileJiggle's persistOrder() used a plain, non-elevation-aware fetch()
// for POST /api/buttons/reorder — a cashier's reorder would get a 200
// response carrying the elevation-prompt HTML (needsElevation), which the
// raw fetch's `res.ok` check treated as unconditional success: the reorder
// was silently NOT persisted, and no PIN prompt was ever shown. Fixed by
// routing through the same window.utPostWithElevation helper
// buttons_admin.html's own (Designer-side) persistOrder already uses for
// this identical route. This spec drives the fixed path for real, through
// a real browser, so a future regression on either side breaks a test
// here rather than only being caught by a Go-level gate test blind to the
// client-side response handling.
//
// Needs the `auth` project — a real cashier-vs-manager session — same
// reasoning tile-sheet-locked-cashier-2312.spec.ts's own retired version
// documented for itself.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier2312${RUN}`;
const CASHIER_PIN = '135790';
const ITEM = { name: `Jiggle2312 Item ${RUN}`, sku: `JIG2312${RUN}` };
// A second tile purely so the grid's own reorder has somewhere real to
// move the first tile to.
const FILLER_ITEM = { name: `Jiggle2312 Filler ${RUN}`, sku: `JIG2312F${RUN}` };

function center(box: { x: number; y: number; width: number; height: number }) {
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

// A real long-press: pointerdown, hold past app.js's 500ms threshold, then
// release without moving — the jiggle-mode-entering path. Mirrors
// sell-tile-jiggle-mode-2339.spec.ts's own identical helper (that file has
// no exported symbols to import from).
async function longPress(tile: Locator) {
  const page = tile.page();
  const box = (await tile.boundingBox())!;
  const c = center(box);
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

async function seedTiles(page: Page): Promise<void> {
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

test.describe('Jiggle-mode edit gated by catalog_management for a cashier (ut-docs#2312)', () => {
  test('remove badge and reorder both land on the real manager-PIN prompt, never a silent no-op', async ({ page }) => {
    await ensureOperator(page); // admin — first-boot wizard or PIN re-login
    // ut-docs#2499: this spec is about the quick-button strip's jiggle
    // mode. It runs against the static AUTH till (not a worker till, which
    // boots in strip mode by itself — see worker-till.ts), so it picks the
    // strip explicitly, as the admin session that can (a cashier would get
    // the elevation prompt).
    await setBrowsingMode(page, 'strip_overflow');
    await seedTiles(page);
    await createCashier(page);
    await loginAsCashier(page);

    await page.goto('/');
    // This item is uncategorized. The auth till is shared with sibling
    // auth specs, so whether it has any categories depends on run order:
    // with none, the strip renders the flat, tab-less grid (since
    // ut-docs#2613 retired the All tab — the only reason a single-bucket
    // till used to get a tab bar); with some, select the item's own
    // "Uncategorized" tab so its panel is the visible one. Scope to
    // #buttons-grid (never a search result) either way.
    await expect(page.locator('#buttons-grid')).toBeVisible();
    const uncategorizedTab = page.getByRole('tab', { name: 'Uncategorized' });
    if ((await page.locator('.products .tab-bar').count()) > 0) {
      await uncategorizedTab.click();
    }
    const tile = page.locator(`#buttons-grid .btn-tile[data-name="${ITEM.name}"]`);
    await expect(tile).toBeVisible();

    await longPress(tile);
    const grid = page.locator('#buttons-grid');
    await expect(grid).toHaveClass(/jiggle-mode/);

    // Reorder via keyboard (ArrowRight moves the focused tile later within
    // its category) — deterministic and avoids simulating a full pointer
    // drag for what this spec actually needs to prove: the PERSIST step
    // hits the real elevation gate, not the drag mechanics themselves
    // (already covered end-to-end, as a manager, by
    // sell-tile-jiggle-mode-2339.spec.ts).
    await tile.focus();
    await page.keyboard.press('ArrowRight');

    // Done triggers persistOrder() -> POST /api/buttons/reorder. A cashier
    // lacking catalog_management must land on the real elevation dialog,
    // never a silent (wrongly-"successful") no-op.
    await page.locator('[data-testid="jiggle-done"]').click();
    await expect(page.locator('#elevation-modal')).toBeVisible();
    await expect(page.locator('#elevation-modal')).toContainText(/PIN|elevation/i);

    // Approve with the ADMIN's own PIN — proves the full checkOrElevate
    // round trip: the retry actually persists the reorder this time.
    const pinInput = page.locator('#elevation-modal input[name="override_pin"]');
    await pinInput.fill(ADMIN_PIN);
    await page.locator('#elevation-modal button[type=submit]').click();
    await expect(page.locator('#elevation-modal')).toBeHidden();
    await expect(grid).not.toHaveClass(/jiggle-mode/);

    // Re-enter edit mode and exercise the remove badge the same way.
    const tileAgain = page.locator(`#buttons-grid .btn-tile[data-name="${ITEM.name}"]`);
    await longPress(tileAgain);
    await expect(grid).toHaveClass(/jiggle-mode/);
    const removeBadge = tileAgain
      .locator('xpath=..')
      .locator('[data-testid="tile-badge-remove"]');
    await expect(removeBadge).toBeVisible();
    page.once('dialog', (d) => d.accept()); // hx-confirm
    await removeBadge.click();
    await expect(page.locator('#elevation-modal')).toBeVisible();
    await expect(page.locator('#elevation-modal')).toContainText(/PIN|elevation/i);

    // Cancel this one — proves a cashier can back out without the removal
    // silently applying anyway.
    await page.locator('#elevation-modal').getByRole('button', { name: /cancel/i }).click();
    await expect(page.locator('#elevation-modal')).toBeHidden();
    await expect(page.locator(`#buttons-grid .btn-tile[data-name="${ITEM.name}"]`)).toBeVisible();
  });
});
