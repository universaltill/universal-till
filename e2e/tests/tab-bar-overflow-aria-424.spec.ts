import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, waitForStableLayout, setBrowsingMode, hideUncategorizedStrays } from './helpers';

// ut-docs#424: both `.tab-bar` instances (tender Pay/Split, sale-screen
// category tabs) get overflow handling for many tabs plus the full
// WAI-ARIA tabs pattern (aria-selected, role=tabpanel, aria-controls,
// roving tabindex, Left/Right arrow-key navigation). Filed from the
// ut-docs#418 review — #418 made sale-screen tab count shop-configurable
// for the first time, which is what makes the overflow case reachable;
// the ARIA gaps predate #418 on the tender bar too.

const CATEGORY_COUNT = 12;

type OverflowItem = { name: string; sku: string; barcode: string; category: string };

// tag/barcodePrefix keep the two tests below on disjoint SKUs/barcodes/
// categories — reusing the same ones would have the second test's import
// silently update the first test's already-deactivated (but not deleted)
// rows instead of creating fresh active ones, since catalog import upserts
// by SKU/barcode.
function overflowItems(tag: string, barcodePrefix: string): OverflowItem[] {
  return Array.from({ length: CATEGORY_COUNT }, (_, i) => {
    const n = String(i + 1).padStart(2, '0');
    return {
      name: `Overflow ${tag} Item ${n}`,
      sku: `OVF${tag}${n}`,
      barcode: `${barcodePrefix}${n}`,
      category: `Overflow ${tag} Cat ${n}`,
    };
  });
}

function overflowCsv(items: OverflowItem[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

// The sale-screen category tab bar groups SHORTCUT buttons
// (data.ShortcutsRepo — the Designer's curated grid), not every catalog
// item — so a plain catalog import (which only creates rows in `items`)
// never shows up as a tab on its own. This drives the same two-step flow
// the Designer's UI does: import the catalog rows, then add each one as a
// shortcut via /api/buttons/add (itemId/label/code, mirroring
// SearchResult.AddVals in internal/ui/buttons.go), which is what actually
// makes BuildCategoryGroups bucket it under its category's tab.
async function seedOverflowCategories(page: Page, fileName: string, items: OverflowItem[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: fileName,
    mimeType: 'text/csv',
    buffer: Buffer.from(overflowCsv(items)),
  });
  // The Import button sets the hidden commit=1 field before submit (see
  // catalog-import-friendly-errors.spec.ts) — no separate preview step.
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

async function cleanupOverflowItems(page: Page, items: OverflowItem[]) {
  // Same pollution rule as catalog-import-friendly-errors.spec.ts: undo the
  // parts of setup that would otherwise carry into other specs. The
  // shortcut button has to be removed explicitly — ShortcutsRepo.
  // LoadButtons has no active-item filter, so deactivating the catalog
  // item alone would leave a dangling button on the sale screen forever.
  // The 12 categories each test's import creates are left behind — with
  // no button in them (BuildCategoryGroups) they render no tab, and the
  // e2e server's DB is a fresh temp dir per run-till.sh invocation, so
  // nothing outlives the run; not worth a third API round-trip to remove.
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

test.describe('tab-bar overflow + ARIA tabs pattern (ut-docs#424)', () => {
  test.describe('sale-screen category tabs', () => {
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
    let items: OverflowItem[] = [];
    test.afterEach(async ({ page }) => {
      if (items.length) await cleanupOverflowItems(page, items);
      items = [];
    });

    test('12+ categories stay on ONE row and never scroll, leaving product tiles visible (ut-docs#2173/#993, ut-docs#2307)', async ({ page }) => {
      // ut-docs#2541: strays from other specs would add an "uncategorized" tab.
      await hideUncategorizedStrays(page);
      // ut-docs#424 originally required this exact tab bar to WRAP onto
      // multiple rows for a large category count (flex-wrap: wrap) — see
      // git history for that version of this test. ut-docs#2173 reversed
      // that, FOR THIS ONE TAB BAR ONLY (the sale-screen category strip,
      // `.products .tab-bar`): at 1024x600 with 12 categories the old
      // wrapping bar alone ran 247px over 5 rows and left ZERO product
      // tiles visible on the kiosk floor (ut-docs#993) — it became a
      // single-row, horizontally-SCROLLABLE strip instead. ut-docs#2307
      // went one step further and removed the scrolling too: a category
      // tab that doesn't fit the one row is hidden (not scrolled to) and
      // reachable instead through the trailing "..." button's own sheet
      // (full coverage: sale-screen-category-strip-overflow-2307.spec.ts).
      // What's still #424's own AC and still worth pinning here: whatever
      // IS on screen stays on exactly one row (measured against only the
      // VISIBLE tabs — a hidden/display:none one reads an all-zero rect,
      // which would otherwise misread as a second "row" at top:0), no tab
      // wraps its own label, and the row freeing up space actually leaves
      // a product tile visible. The tender Pay/Split bar's own `.tab-bar`
      // keeps its original wrap behaviour untouched (see the generic
      // `.tab-bar` tests further down this file, and app.css's own
      // comment on why that rule is never scoped away from by this card).
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 1024, height: 600 });
      items = overflowItems('Wrap', '71');

      await seedOverflowCategories(page, 'import-424-wrap.csv', items);

      await page.goto('/');
      const tabBar = page.locator('.products .tab-bar');
      await expect(tabBar).toBeVisible();
      // The DOM still holds every tab regardless of which ones are shown
      // (see ut-docs#2307's own coverage for the fit/overflow split
      // itself) — a plain CSS class locator matches a hidden tab just as
      // well as a visible one, so this count is unaffected by that card.
      await expect(tabBar.locator('.tab')).toHaveCount(CATEGORY_COUNT + 4, { timeout: 10_000 }); // + seeded Food/Drinks/Household/Produce (ut-docs#2498: items-only categories now show); no All tab since ut-docs#2613
      await waitForStableLayout(page, '.products .tab-bar, .products .tab-bar .tab');

      // ut-docs#2307: never overflows its own box — nothing left to
      // scroll to.
      const overflowPx = await tabBar.evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(overflowPx, `tab-bar must not overflow, got ${overflowPx}px`).toBeLessThanOrEqual(1);

      const boxes = await tabBar.locator('.tab:not([hidden])').evaluateAll((els) =>
        els.map((el) => {
          const r = (el as HTMLElement).getBoundingClientRect();
          return { top: Math.round(r.top), height: Math.round(r.height) };
        }),
      );
      expect(boxes.length, 'at least the fixed tabs should be visible').toBeGreaterThan(0);

      // ut-docs#2173: every VISIBLE tab shares the SAME top offset — one
      // row, not many.
      const rowsSeen = new Set(boxes.map((b) => b.top));
      expect(rowsSeen.size, `sale-screen tab bar should stay on exactly one row, saw tops: ${JSON.stringify(boxes)}`).toBe(1);

      // #424's own AC: every visible tab stays a single readable line,
      // never a squashed multi-line label.
      //
      // ut-docs#2173's independent review pointed out that the height check
      // below, kept from the pre-#2173 version of this test, can no longer
      // fail: with flex-wrap: nowrap on the container AND the pre-existing
      // white-space: nowrap on .tab, a tab CANNOT wrap its own label, so the
      // assertion is now tautological. Keeping it is still right (it would
      // catch a future change that reintroduces wrapping), but on its own it
      // is no longer live coverage of this AC — so it is paired with the
      // check that actually can fail now.
      //
      // What can go wrong in the nowrap world is the opposite failure: the
      // label CLIPPED inside its own tab box rather than wrapped. A tab
      // whose content is wider than its own content box is exactly that, and
      // it is what "readable" means once wrapping is off the table.
      for (const b of boxes) {
        expect(b.height, 'a tab must not have wrapped its own label across multiple lines').toBeLessThan(60);
      }
      const clipped = await tabBar.locator('.tab:not([hidden])').evaluateAll((els) =>
        els
          .map((el) => ({ label: (el.textContent || '').trim(), scrollW: el.scrollWidth, clientW: el.clientWidth }))
          // 1px of tolerance for sub-pixel layout rounding.
          .filter((t) => t.scrollW > t.clientW + 1),
      );
      expect(clipped, `every visible tab must show its whole label — these are clipped inside their own box: ${JSON.stringify(clipped)}`).toEqual([]);

      // ut-docs#993: the tab bar being single-row is only useful if it
      // actually frees up room for product tiles. Assert real geometry,
      // not just element presence — the first tile must sit ENTIRELY
      // inside the .products panel's own visible box, with no scrolling
      // needed to see it.
      const tabBarBox = await tabBar.boundingBox();
      expect(tabBarBox, 'tab bar must have a measurable box').toBeTruthy();
      // A generous single-row bound: real single-row tab heights on this
      // UI run well under 3rem (~48px); 60px leaves headroom without
      // tolerating a second row sneaking back in.
      expect(tabBarBox!.height, `tab bar height should be single-row, measured ${tabBarBox!.height}px`).toBeLessThan(60);

      const panelBox = await page.locator('.products').boundingBox();
      expect(panelBox, '.products panel must have a measurable box').toBeTruthy();
      const firstTile = page.locator('.products .btn-tile').first();
      await expect(firstTile).toBeVisible();
      const tileBox = await firstTile.boundingBox();
      expect(tileBox, 'first product tile must have a measurable box').toBeTruthy();

      expect(
        tileBox!.y,
        `first tile top (${tileBox!.y}) should be >= .products panel top (${panelBox!.y}) -- tab bar: ${JSON.stringify(tabBarBox)}, panel: ${JSON.stringify(panelBox)}, tile: ${JSON.stringify(tileBox)}`,
      ).toBeGreaterThanOrEqual(panelBox!.y);
      expect(
        tileBox!.y + tileBox!.height,
        `first tile bottom (${tileBox!.y + tileBox!.height}) should be <= .products panel bottom (${panelBox!.y + panelBox!.height}) -- tab bar: ${JSON.stringify(tabBarBox)}, panel: ${JSON.stringify(panelBox)}, tile: ${JSON.stringify(tileBox)}`,
      ).toBeLessThanOrEqual(panelBox!.y + panelBox!.height);

      assertClean();
    });

    test('category tabs carry the full WAI-ARIA tabs pattern and respond to arrow keys', async ({ page }) => {
      const assertClean = watchConsole(page);
      items = overflowItems('Aria', '72');

      await seedOverflowCategories(page, 'import-424-aria.csv', items);

      await page.goto('/');
      const tabBar = page.locator('.products .tab-bar');
      // getByRole('tab') only resolves elements actually in the
      // accessibility tree — since ut-docs#2307 an overflowed category tab
      // is a real `hidden` attribute (display:none), which drops it out of
      // that tree same as it drops out of the page visually, so this is
      // exactly the VISIBLE tab count now (fixed tabs + however many
      // categories fit at this viewport), not the full 12-category import.
      // The fit-vs-overflow split itself is sale-screen-category-strip-
      // overflow-2307.spec.ts's own job, not this file's — this test only
      // needs "more than one", to have something real to arrow-key between.
      const tabs = tabBar.getByRole('tab');
      const count = await tabs.count();
      expect(count, 'at least the fixed tabs plus one real category should be reachable').toBeGreaterThan(1);

      // This test's aria-controls/panel-identity assertions below need a
      // REAL category active. Since ut-docs#2613 retired ut-docs#2212's
      // All tab (which owned no single panel), the first tab already is
      // one and is selected by default; click it anyway so the starting
      // state doesn't depend on that default — the roving-tabindex/
      // arrow-key mechanics under test are unaffected by which tab starts
      // active.
      await tabs.nth(0).click();

      // Exactly one tab is the roving-tabindex stop and carries aria-selected.
      await expect(tabBar.locator('[aria-selected="true"]')).toHaveCount(1);
      await expect(tabBar.locator('[tabindex="0"]')).toHaveCount(1);

      const activeId = await tabBar.locator('[aria-selected="true"]').getAttribute('id');
      // A fixed-id locator, not a live "[aria-selected=true]" re-query —
      // that query would resolve to whichever tab is CURRENTLY selected,
      // which is exactly what's under test below and must not shift out
      // from under its own "now false" assertion.
      const wasActive = tabBar.locator(`#${activeId}`);
      const panelId = await wasActive.getAttribute('aria-controls');
      expect(panelId).toBeTruthy();
      const panel = page.locator(`#${panelId}`);
      await expect(panel).toHaveAttribute('role', 'tabpanel');
      await expect(panel).toHaveAttribute('aria-labelledby', activeId!);
      await expect(panel).toBeVisible();

      // ArrowRight moves both focus and selection to the next tab in DOM order.
      const activeIndex = await tabs.evaluateAll((els, id) => els.findIndex((el) => el.id === id), activeId);
      await wasActive.focus();
      await page.keyboard.press('ArrowRight');

      const nextTab = tabs.nth((activeIndex + 1) % count);
      await expect(nextTab).toHaveAttribute('aria-selected', 'true');
      await expect(nextTab).toHaveAttribute('tabindex', '0');
      await expect(nextTab).toHaveClass(/active/);
      await expect(nextTab).toBeFocused();
      await expect(wasActive).toHaveAttribute('aria-selected', 'false');
      await expect(wasActive).toHaveAttribute('tabindex', '-1');

      const nextPanelId = await nextTab.getAttribute('aria-controls');
      await expect(page.locator(`#${nextPanelId}`)).toBeVisible();

      // ArrowLeft from the first tab wraps around to the last.
      await tabs.first().focus();
      await page.keyboard.press('ArrowLeft');
      await expect(tabs.last()).toHaveAttribute('aria-selected', 'true');
      await expect(tabs.last()).toBeFocused();

      assertClean();
    });
  });

  test('tender Pay/Split tabs carry the full WAI-ARIA tabs pattern and respond to arrow keys', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // ut-docs#1252: the Pay/Split tab bar now lives inside the
    // #payment-overlay dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();

    const tabBar = page.locator('.tender .tab-bar');
    const payTab = tabBar.getByRole('tab', { name: 'Pay' });
    const splitTab = tabBar.getByRole('tab', { name: 'Split' });

    await expect(payTab).toHaveAttribute('aria-selected', 'true');
    await expect(payTab).toHaveAttribute('tabindex', '0');
    await expect(splitTab).toHaveAttribute('aria-selected', 'false');
    await expect(splitTab).toHaveAttribute('tabindex', '-1');

    const payPanelId = await payTab.getAttribute('aria-controls');
    const splitPanelId = await splitTab.getAttribute('aria-controls');
    await expect(page.locator(`#${payPanelId}`)).toHaveAttribute('role', 'tabpanel');
    await expect(page.locator(`#${splitPanelId}`)).toHaveAttribute('role', 'tabpanel');

    await payTab.focus();
    await page.keyboard.press('ArrowRight');
    await expect(splitTab).toHaveAttribute('aria-selected', 'true');
    await expect(splitTab).toBeFocused();
    await expect(page.locator(`#${splitPanelId}`)).toBeVisible();

    // Wraps back around: ArrowRight again from the last tab returns to Pay.
    await page.keyboard.press('ArrowRight');
    await expect(payTab).toHaveAttribute('aria-selected', 'true');
    await expect(payTab).toBeFocused();

    assertClean();
    // e2e/README: a spec that adds basket items must complete its sale or
    // explicitly clear it — this test (outside the nested describe's own
    // afterEach) never did either before ut-docs#1984's scan-first change.
    await page.request.post('/api/pos/reset');
  });

  test('arrow keys follow the VISUAL direction under RTL, not raw DOM order', async ({ page }) => {
    // ArrowRight/Left must keep moving toward the visually next/previous
    // tab (WAI-ARIA APG) — under dir="rtl" that's the OPPOSITE of DOM
    // order, since Pay (DOM-first) renders on the right and Split
    // (DOM-second) renders on the left. A naive @keydown.right ==
    // "always step +1 through the DOM" would send focus the wrong way.
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    // ut-docs#1984: scan first — Payment is disabled on an empty basket.
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // ut-docs#1252: the Pay/Split tab bar now lives inside the
    // #payment-overlay dialog, opened by the .payment-trigger button.
    await page.getByTestId('payment-open').click();

    const tabBar = page.locator('.tender .tab-bar');
    const tabs = tabBar.getByRole('tab');
    await expect(tabs).toHaveCount(2);
    const [domFirst, domSecond] = [tabs.nth(0), tabs.nth(1)];
    const [firstBox, secondBox] = await Promise.all([domFirst.boundingBox(), domSecond.boundingBox()]);
    expect(firstBox).toBeTruthy();
    expect(secondBox).toBeTruthy();
    expect(firstBox!.x, 'DOM-first tab should render visually to the right under RTL').toBeGreaterThan(secondBox!.x);

    // ArrowRight from the DOM-first (visually rightmost) tab must move to
    // the visually-left neighbor, i.e. domSecond — not stay put / wrap.
    await domFirst.focus();
    await page.keyboard.press('ArrowRight');
    await expect(domSecond).toBeFocused();
    await expect(domSecond).toHaveAttribute('aria-selected', 'true');

    // ArrowLeft from there moves back to the visually-right domFirst.
    await page.keyboard.press('ArrowLeft');
    await expect(domFirst).toBeFocused();
    await expect(domFirst).toHaveAttribute('aria-selected', 'true');

    assertClean();
    // e2e/README: a spec that adds basket items must complete its sale or
    // explicitly clear it — see the matching note on the test above.
    await page.request.post('/api/pos/reset');
  });
});
