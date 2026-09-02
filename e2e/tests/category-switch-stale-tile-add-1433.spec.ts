import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#1433: on the TECLAST tablet, tapping the only tile in the
// default category added it correctly, then switching to the "Kuchen"
// category tab and, several seconds later, tapping the tile now occupying
// the SAME GRID POSITION the first tile had added the FIRST (now-stale)
// item again instead of the one actually visible/tapped. The grid itself
// is never re-fetched on a tab switch (buttons.html renders every
// category's tiles once on page load; Alpine's `x-show` just toggles which
// panel is visible) and each .btn-tile carries its own item `code` baked
// into `hx-vals` at render time, so on paper a tap should always resolve to
// whatever tile the operator is actually looking at. This spec seeds two
// categories with exactly one item each — so category B's only tile always
// renders at the exact screen position category A's only tile occupied —
// adds category A's item, switches to category B, and taps the position
// category A's tile used to occupy, at both a fast and a slow (~5s,
// matching the original report's t=1s tab-switch -> t=8s tap) delay.

type Item = { name: string; sku: string; barcode: string; category: string };

// tag/barcodePrefix keep the fast- and slow-tap variants below on disjoint
// SKUs/barcodes/categories — reusing the same ones would have the second
// variant's import silently update the first variant's already-deactivated
// (afterEach) rows instead of creating fresh active ones, since catalog
// import upserts by SKU/barcode (same pitfall tab-bar-overflow-aria-424
// .spec.ts's own overflowItems() comment documents).
function itemsFor(tag: string, barcodePrefix: string): [Item, Item] {
  return [
    { name: `Stale1433 ${tag} Item A`, sku: `STALE1433${tag}A`, barcode: `${barcodePrefix}1`, category: `Stale1433 ${tag} Cat A` },
    { name: `Stale1433 ${tag} Item B`, sku: `STALE1433${tag}B`, barcode: `${barcodePrefix}2`, category: `Stale1433 ${tag} Cat B` },
  ];
}

function csvFor(items: Item[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// Mirrors tab-bar-overflow-aria-424.spec.ts's seedOverflowCategories: a
// plain catalog import only creates `items` rows, which BuildCategoryGroups
// never sees — the sale-screen tab bar groups SHORTCUT buttons
// (data.ShortcutsRepo). Import the catalog rows, then add each as a
// shortcut via /api/buttons/add so it actually renders as a tile/tab.
async function seedItems(page: Page, fileName: string, items: Item[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: fileName,
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
    const id = await row.first().getAttribute('data-id');
    const resp = await page.request.post('/api/buttons/add', {
      form: { itemId: id ?? '', label: it.name, code: it.barcode },
    });
    expect(resp.ok(), `add shortcut for ${it.name}`).toBe(true);
  }
}

async function cleanupItems(page: Page, items: Item[]) {
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

// Common setup/teardown/repro steps shared by the fast- and slow-tap
// variants below — only the delay between the tab switch and the tap
// differs between them, per the card's own note that the original bad tap
// (t≈8s) and a later good one (tab -> wait 3s -> tap, different item pair)
// were BOTH past any CSS-transition window, so timing alone isn't proven to
// be the deciding variable; this still covers both ends of the range the
// report gives.
async function runRepro(page: Page, tag: string, barcodePrefix: string, delayMs: number, useTouch = false) {
  const assertClean = watchConsole(page);
  const [ITEM_A, ITEM_B] = itemsFor(tag, barcodePrefix);
  await seedItems(page, `import-1433-${tag}.csv`, [ITEM_A, ITEM_B]);

  await page.goto('/');
  await page.getByRole('tab', { name: ITEM_A.category }).click();

  const tileA = page.locator(`.btn-tile[data-name="${ITEM_A.name}"]`);
  await expect(tileA).toBeVisible();
  const boxA = await tileA.boundingBox();
  expect(boxA, 'category A tile must have a real layout box').toBeTruthy();

  // t=0: tap the only tile in category A — adds it to the basket.
  await tileA.click();
  await expect(page.locator('#basket')).toContainText(ITEM_A.name);

  // t=1s: switch to category B. Pure Alpine (x-show) — no request, no
  // basket re-render — so any earlier /api/pos/scan response is long since
  // settled by the time this fires.
  await page.getByRole('tab', { name: ITEM_B.category }).click();

  const tileB = page.locator(`.btn-tile[data-name="${ITEM_B.name}"]`);
  await expect(tileB).toBeVisible();
  const boxB = await tileB.boundingBox();
  expect(boxB, 'category B tile must have a real layout box').toBeTruthy();

  // Sanity-check the repro's own precondition: category B's only tile
  // renders at the SAME screen position category A's only tile did — the
  // exact "cinnamon cake tile at the same position the Ice Americano tile
  // had" setup from the report. Without this, a coordinate tap below would
  // prove nothing about stale-position resolution.
  expect(Math.round(boxB!.x), 'category B tile should render at the same X as category A\'s did').toBe(
    Math.round(boxA!.x),
  );
  expect(Math.round(boxB!.y), 'category B tile should render at the same Y as category A\'s did').toBe(
    Math.round(boxA!.y),
  );

  await page.waitForTimeout(delayMs);

  // Tap by raw screen COORDINATES, not by re-resolving the "category B
  // tile" locator — an ADB tap (how the original bug was driven) hits
  // whatever the browser's own hit-testing resolves at that point, and
  // Playwright's locator.click() would otherwise re-verify actionability
  // against tileB specifically and mask exactly the "wrong element received
  // the tap" failure mode this bug describes. useTouch dispatches a real
  // touch (Chromium's own touch-to-click synthesis, closer to how ADB's
  // `input tap` drives the WebView) instead of a synthetic mouse click.
  const cx = boxB!.x + boxB!.width / 2;
  const cy = boxB!.y + boxB!.height / 2;
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    useTouch ? page.touchscreen.tap(cx, cy) : page.mouse.click(cx, cy),
  ]);

  try {
    const basket = page.locator('#basket');
    // The correct outcome: category B's item was added, and category A's
    // item is still just the one line the first tap (t=0) created — the
    // reported bug instead left category B's item entirely absent and
    // re-added category A's item as a second unit on its existing line.
    await expect(basket).toContainText(ITEM_B.name);
    const rowA = basket.locator('tr', { has: page.locator('.line-name', { hasText: ITEM_A.name }) });
    await expect(rowA.locator('.qty-input')).toHaveValue('1');

    assertClean();
  } finally {
    await cleanupItems(page, [ITEM_A, ITEM_B]);
  }
}

test.describe('category switch does not resolve a tap to a stale tile (ut-docs#1433)', () => {
  test('fast tap (~100ms after the tab switch)', async ({ page }) => {
    await runRepro(page, 'Fast', '914331', 100);
  });

  test('slow tap (~5s after the tab switch, matching the original t=1s -> t=8s repro)', async ({ page }) => {
    await runRepro(page, 'Slow', '914332', 5000);
  });
});

// A separate describe with hasTouch enabled: dispatches a real touch
// (Chromium's touch->click synthesis) instead of a synthetic mouse click,
// closer to how `adb shell input tap` actually drove the original repro on
// the TECLAST tablet's WebView.
test.describe('category switch does not resolve a tap to a stale tile — real touch dispatch (ut-docs#1433)', () => {
  test.use({ hasTouch: true });

  test('slow tap via touchscreen.tap (~5s after the tab switch)', async ({ page }) => {
    await runRepro(page, 'Touch', '914333', 5000, true);
  });
});
