import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#3073 (product owner + pilot café owner, on the tablet): in the
// category_tabs browsing mode (ut-docs#2499) the category popup
//   1. opens FULL SCREEN -- everything to the inline-end of the nav rail,
//      top to bottom -- so there is room to find the item; the rail itself
//      is never covered, so its status chips stay visible and Lock stays
//      tappable (CLAUDE.md "Offline-first", ut-docs#1999/#2873);
//   2. CLOSES BY ITSELF once the cashier has added an item: a plain item
//      as soon as its line is in the basket, a modifier/variant item only
//      once the picker's Add succeeded (cancelling the picker leaves the
//      popup open), and a refused add leaves it open too;
//   3. still shrinks back into its tile on that close (ADR-0122/0123,
//      ut-docs#2939/#2987): the close goes through dialog.close(), so the
//      shared motion's data-ut-closing is observable.
//
// HONESTY NOTE: driven with Playwright's synthetic pointer in Chromium at
// 1024x600 (the kiosk floor) and 1280x800. How it feels under a finger on
// the pilot tablet is the local hardware lane's to confirm.

type Item = { name: string; sku: string; barcode: string };
const RUN = Date.now().toString(36).toUpperCase();

function makeItems(tag: string) {
  const run = `${RUN}${tag}`;
  const cat = `Pop3073 Cat ${run}`;
  const plain: Item = { name: `Pop3073 Plain ${run}`, sku: `PP3073P${run}`, barcode: `PP3073-P-${run}` };
  const mod: Item = { name: `Pop3073 Mod ${run}`, sku: `PP3073M${run}`, barcode: `PP3073-M-${run}` };
  return { cat, plain, mod, all: [plain, mod] };
}

async function seedItems(page: Page, cat: string, items: Item[]): Promise<Record<string, string>> {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${cat},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-3073-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from('Name,SKU,Barcode,Price,Category,In stock\n' + rows),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  const ids: Record<string, string> = {};
  for (const it of items) {
    ids[it.name] = (await page.locator(`.catalog-row[data-name="${it.name}"]`).first().getAttribute('data-id'))!;
  }
  return ids;
}

// One optional modifier group with one option, through the catalog API
// (same shape as sell-screen-browsing-mode-2499.spec.ts's helper).
async function seedModifier(page: Page, itemId: string, optionName: string): Promise<void> {
  const groupName = `Size ${optionName}`;
  const groupResp = await page.request.post('/api/catalog/modifier-group', {
    form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
  });
  expect(groupResp.ok(), 'create modifier group').toBe(true);
  const html = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`)).text();
  const nameIdx = html.indexOf(`>${groupName}<`);
  expect(nameIdx, 'modifier-groups-panel must contain the new group').toBeGreaterThan(-1);
  const idMatches = [...html.slice(0, nameIdx).matchAll(/data-group-id="([^"]*)"/g)];
  const groupId = idMatches[idMatches.length - 1][1];
  const optResp = await page.request.post('/api/catalog/modifier-option', { form: { groupId, itemId, name: optionName } });
  expect(optResp.ok(), 'create modifier option').toBe(true);
}

async function cleanup(page: Page, ids: Record<string, string>) {
  for (const id of Object.values(ids)) {
    await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  }
}

async function openPopup(page: Page, cat: string) {
  const tile = page.locator('#browsing-category-tiles .category-tile', { hasText: cat });
  await expect(tile).toBeVisible();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/ui/buttons/category?id=')),
    tile.click(),
  ]);
  const modal = page.locator('#category-items-modal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('#category-items-modal-body .btn-tile').first()).toBeVisible();
  await settled(modal);
  return modal;
}

// Motion is on in the close test: wait for a popup's opening zoom to end
// before driving controls inside it (a tap mid-zoom is ADR-0122 §7's
// business, covered by popup-zoom-2944.spec.ts, not this card's).
async function settled(loc: import('@playwright/test').Locator) {
  await expect.poll(() => loc.evaluate((el) => el.getAnimations().length)).toBe(0);
}

// Records whether the popup went through the shared close shrink
// (base.html's popupClose sets data-ut-closing on the SAME element).
async function watchClosing(page: Page) {
  await page.evaluate(() => {
    const dlg = document.getElementById('category-items-modal')!;
    (window as any).__utClosingSeen = false;
    new MutationObserver(() => {
      if (dlg.hasAttribute('data-ut-closing')) (window as any).__utClosingSeen = true;
    }).observe(dlg, { attributes: true, attributeFilter: ['data-ut-closing'] });
  });
}

for (const vp of [{ width: 1024, height: 600 }, { width: 1280, height: 800 }]) {
  test.describe(`ut-docs#3073 category popup at ${vp.width}x${vp.height}`, () => {
    let ids: Record<string, string> = {};
    test.afterEach(async ({ page }) => {
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await cleanup(page, ids);
      ids = {};
      await setBrowsingMode(page, 'strip_overflow'); // the worker till's own mode
    });

    test('opens full screen beside the rail; the rail stays uncovered and lifted above the scrim', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      const { cat, all } = makeItems(`F${vp.width}`);
      ids = await seedItems(page, cat, all);
      await setBrowsingMode(page, 'category_tabs');
      await page.goto('/');
      const modal = await openPopup(page, cat);

      const geo = await page.evaluate(() => {
        const d = document.getElementById('category-items-modal')!.getBoundingClientRect();
        const nav = document.querySelector('.nav')!.getBoundingClientRect();
        const body = document.getElementById('category-items-modal-body')!.getBoundingClientRect();
        const close = document.querySelector('#category-items-modal .modifier-actions .btn')!.getBoundingClientRect();
        return { d: { l: d.left, t: d.top, r: d.right, b: d.bottom }, navRight: nav.right,
          bodyH: body.height, closeBottom: close.bottom, vw: document.documentElement.clientWidth, vh: window.innerHeight };
      });
      // Full screen: from the rail's edge to the viewport's end, top to bottom.
      expect(Math.abs(geo.d.l - geo.navRight), `popup starts at the rail's edge (${JSON.stringify(geo)})`).toBeLessThanOrEqual(1);
      expect(Math.abs(geo.d.r - geo.vw), 'popup reaches the inline end').toBeLessThanOrEqual(1);
      expect(Math.abs(geo.d.t), 'popup starts at the top').toBeLessThanOrEqual(1);
      expect(Math.abs(geo.d.b - geo.vh), 'popup reaches the bottom').toBeLessThanOrEqual(1);
      // The item list takes the room (not the old 60vh cap) and Close is on screen.
      expect(geo.bodyH, 'item list uses most of the height').toBeGreaterThan(geo.vh * 0.6);
      expect(geo.closeBottom, 'Close stays on screen').toBeLessThanOrEqual(geo.vh);

      // The rail is not under the popup (the geometry above: the popup
      // starts at its edge) and is the scrim's lifted rail -- z 460 above
      // #ut-scrim, the state in which popup-scrim-2873.spec.ts and
      // nav-rail-lock-reachable-1346.spec.ts (auth project -- this default
      // project has no session, so no Lock form) prove Lock stays live.
      const rail = await page.evaluate(() => {
        const nav = document.querySelector('.nav') as HTMLElement;
        const r = nav.getBoundingClientRect();
        return { scrimOn: document.documentElement.classList.contains('ut-scrim-on'), z: getComputedStyle(nav).zIndex,
          visible: r.width > 0 && r.height > 0 && getComputedStyle(nav).visibility !== 'hidden' };
      });
      expect(rail, 'the rail stays visible and lifted above the scrim beside the popup').toEqual({ scrimOn: true, z: '460', visible: true });
      await modal.getByRole('button', { name: 'Close' }).click();
      await expect(modal).toBeHidden();
      assertClean();
    });

    test('closes by itself once an item is added (plain now, modifier after the picker), with the close motion', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      const { cat, plain, mod, all } = makeItems(`C${vp.width}`);
      ids = await seedItems(page, cat, all);
      const optionName = `Large 3073 ${vp.width} ${RUN}`;
      await seedModifier(page, ids[mod.name], optionName);
      await setBrowsingMode(page, 'category_tabs');
      // The suite runs as a reduced-motion user (fixtures.ts), under which
      // the shared popup motion is off; the close shrink is part of this AC.
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await page.goto('/');

      // Plain item: the line lands and the popup closes, shrinking into its tile.
      let modal = await openPopup(page, cat);
      await watchClosing(page);
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
        modal.locator('#category-items-modal-body .btn-tile', { hasText: plain.name }).click(),
      ]);
      await expect(page.locator('#basket')).toContainText(plain.name);
      await expect(modal).toBeHidden();
      expect(await page.evaluate(() => (document.getElementById('category-items-modal') as HTMLDialogElement).open)).toBe(false);
      expect(await page.evaluate(() => (window as any).__utClosingSeen), 'the shared close shrink ran').toBe(true);

      // Modifier item: the picker opens over the popup; cancelling it keeps
      // the popup open ...
      modal = await openPopup(page, cat);
      const modifierModal = page.locator('#modifier-modal');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
        modal.locator('#category-items-modal-body .btn-tile', { hasText: mod.name }).click(),
      ]);
      await expect(modifierModal).toBeVisible();
      await settled(modifierModal);
      await page.keyboard.press('Escape');
      await expect(modifierModal).toBeHidden();
      await expect(modal).toBeVisible();

      // ... and confirming it adds the line and closes both.
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
        modal.locator('#category-items-modal-body .btn-tile', { hasText: mod.name }).click(),
      ]);
      await expect(modifierModal).toBeVisible();
      await settled(modifierModal);
      await modifierModal.locator('.modifier-option', { hasText: optionName }).locator('input').check();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers')),
        modifierModal.getByRole('button', { name: /Add to cart/i }).click(),
      ]);
      await expect(page.locator('#basket')).toContainText(optionName);
      await expect(modifierModal).toBeHidden();
      await expect(modal).toBeHidden();

      // Reopening starts clean: nothing is left armed, so an item added by
      // other means (a scan-row / barcode add -- the same /api/pos/scan ->
      // #basket swap, issued here the way app.js replays one) leaves it open.
      modal = await openPopup(page, cat);
      await page.evaluate((code) => (window as any).htmx.ajax('post', '/api/pos/scan', { target: '#basket', swap: 'outerHTML', values: { code } }), plain.barcode);
      await expect(page.locator('#basket [data-testid="basket-count"]')).toHaveText('3');
      await page.waitForTimeout(300);
      await expect(modal).toBeVisible();
      await modal.getByRole('button', { name: 'Close' }).click();
      await expect(modal).toBeHidden();
      await page.request.post('/api/pos/reset');
      assertClean();
    });
  });
}

// A refused add must leave the popup open, so the cashier sees the refusal
// and can pick something else. /api/pos/scan refuses with a 200 that
// re-renders #basket carrying an error notice (basket.html's
// .pos-notice.error, role=alert -- e.g. modifiers.variant_unavailable,
// pos.toast.customer_not_found), not with a 4xx; stubbed here in exactly
// that shape because no seedable item is refused deterministically.
test('ut-docs#3073: a refused add leaves the category popup open', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  const { cat, plain, all } = makeItems('R');
  const ids = await seedItems(page, cat, all);
  await setBrowsingMode(page, 'category_tabs');
  await page.goto('/');
  const modal = await openPopup(page, cat);
  await page.route('**/api/pos/scan', (route) => route.fulfill({
    status: 200,
    contentType: 'text/html; charset=utf-8',
    body: '<div class="basket" id="basket" data-lines-count="0"><div class="pos-notice error" id="toast-message" role="alert"><span class="notice-text">Refused 3073</span></div></div>',
  }));
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    modal.locator('#category-items-modal-body .btn-tile', { hasText: plain.name }).click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Refused 3073');
  await page.waitForTimeout(300);
  await expect(modal).toBeVisible();
  await page.unroute('**/api/pos/scan');
  await modal.getByRole('button', { name: 'Close' }).click();
  await expect(modal).toBeHidden();
  await cleanup(page, ids);
  await setBrowsingMode(page, 'strip_overflow');
  assertClean();
});

// <=480px: the rail is a top bar, so the full-screen popup starts under it
// (never over it -- its chips stay visible) and runs to the bottom.
test('ut-docs#3073: at phone width the popup fills the screen below the top bar', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 360, height: 800 });
  const { cat, all } = makeItems('P');
  const ids = await seedItems(page, cat, all);
  await setBrowsingMode(page, 'category_tabs');
  await page.goto('/');
  const modal = await openPopup(page, cat);
  const g = await page.evaluate(() => {
    const d = document.getElementById('category-items-modal')!.getBoundingClientRect();
    const nav = document.querySelector('.nav')!.getBoundingClientRect();
    return { l: d.left, r: d.right, t: d.top, b: d.bottom, navBottom: nav.bottom, vw: document.documentElement.clientWidth, vh: window.innerHeight };
  });
  expect(g.t, `popup starts below the top bar (${JSON.stringify(g)})`).toBeGreaterThanOrEqual(g.navBottom - 1);
  expect(Math.abs(g.l), 'popup starts at the inline start').toBeLessThanOrEqual(1);
  expect(Math.abs(g.r - g.vw), 'popup reaches the inline end').toBeLessThanOrEqual(1);
  expect(Math.abs(g.b - g.vh), 'popup reaches the bottom').toBeLessThanOrEqual(1);
  await modal.getByRole('button', { name: 'Close' }).click();
  await expect(modal).toBeHidden();
  await cleanup(page, ids);
  await setBrowsingMode(page, 'strip_overflow');
  assertClean();
});

// The #2525 stale-tile answer (the item was deactivated/recoded on another
// device while the popup was open): 200, the basket with an info notice and
// HX-Trigger: buttons-changed, nothing added -- the popup stays open.
test('ut-docs#3073: a stale tile leaves the category popup open', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  const { cat, plain, all } = makeItems('S');
  const ids = await seedItems(page, cat, all);
  await setBrowsingMode(page, 'category_tabs');
  await page.goto('/');
  const modal = await openPopup(page, cat);
  await page.request.post('/api/catalog/item/deactivate', { form: { id: ids[plain.name] } });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    modal.locator('#category-items-modal-body .btn-tile', { hasText: plain.name }).click(),
  ]);
  await page.waitForTimeout(300);
  await expect(modal, 'the popup stays open on a stale tile').toBeVisible();
  await expect(page.locator('#basket #toast-message')).toHaveCount(1);
  await modal.getByRole('button', { name: 'Close' }).click();
  await expect(modal).toBeHidden();
  await cleanup(page, ids);
  await setBrowsingMode(page, 'strip_overflow');
  assertClean();
});
