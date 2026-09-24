import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2307: the sell-screen category strip must never scroll any more
// (product owner, direct: "I prefer to have a ... button at the right;
// when clicked it shows all the categories to select"). It now shows as
// many category tabs as actually fit the row, plus the fixed Categories/
// All tabs (always first, untouched by this card — see
// sale-screen-category-tabs-search-418.spec.ts for their own coverage),
// and reveals a trailing "..." button only once at least one category tab
// genuinely doesn't fit.
//
// The demo catalogue (seeded fresh into every worker's own till —
// worker-till.ts's `go run ./e2e/seed_demo`) ships four top-level
// categories (Food/Drinks/Household/Produce — see
// internal/data/seeddata/demo_catalogue.sql's own parent_id column). Only
// Drinks and Food own a quick button, but since ut-docs#2498
// BuildCategoryGroups (internal/ui/buttons.go) also keeps a category with
// active items and no quick buttons, so Household/Produce show as tabs
// too — all FOUR render. A category with neither is still pruned — the
// pruning this file's own cleanup below relies on (deactivating the
// created items drops each created category's active-item count to 0).
// That is this card's own small-shop regression case for free, with
// nothing to add or clean up.
//
// The "many categories" cases below create their OWN categories (each
// with one active item as its quick button — BuildCategoryGroups,
// internal/ui/buttons.go, only ever renders a category as a tab once it
// owns at least one) via the app's own endpoints, the same
// create-through-the-API-then-read-the-id-back pattern
// sell-tile-jiggle-mode-locked-cashier-2312.spec.ts and
// categories-editor-2284.spec.ts already use — not a new fixture
// mechanism. Every created item is DEACTIVATED again at the end of each
// test (POST /api/catalog/item/deactivate): ShortcutsRepo.LoadButtons
// only joins active items, so this prunes the category right back out of
// BuildCategoryGroups's output, restoring the shared worker till's strip
// to Food/Drinks/Household/Produce for whichever spec file runs on this
// worker next — this card's own strip is the one thing a LOT of other
// specs assume renders a short, unsurprising tab set.
test.describe('sale screen category strip overflow (ut-docs#2307)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const RUN = Date.now().toString(36).toUpperCase();
  function catName(tag: string, i: number): string {
    return `Ovf2307 ${tag} ${RUN} ${String(i).padStart(2, '0')}`;
  }
  function itemName(tag: string, i: number): string {
    return `Ovf2307 Item ${tag} ${RUN} ${String(i).padStart(2, '0')}`;
  }

  type Cat = { id: string; name: string; itemId: string; itemName: string };

  // Creates `count` fresh top-level categories, each with one active item
  // assigned to it and added as that item's quick button — see this
  // file's own top comment for why both steps are needed before a
  // category shows up as a tab at all. Two page.goto()s total (not one
  // per category): every category/item is POSTed first, then their ids
  // are all read back in a single page load each, the same
  // create-in-bulk-then-read-once shape as this file's own use of
  // page.request over a Chromium round trip for a plain server mutation.
  async function createOverflowCategories(page: Page, tag: string, count: number): Promise<Cat[]> {
    const catNames = Array.from({ length: count }, (_, i) => catName(tag, i + 1));
    for (const name of catNames) {
      const resp = await page.request.post('/api/categories', { form: { name }, maxRedirects: 0 });
      expect([200, 303], `create category ${name}`).toContain(resp.status());
    }
    await page.goto('/categories');
    const catIds: string[] = [];
    for (const name of catNames) {
      // data-field-name is an EXACT prefill attribute (record-dialog.js),
      // not a substring match like Playwright's hasText — load-bearing
      // here since these zero-padded names would otherwise collide
      // (e.g. "... 01" is a hasText substring of nothing here, but this
      // avoids relying on that at all).
      const row = page.locator(`.category-row[data-field-name="${name}"]`);
      await expect(row, `category row for ${name}`).toHaveCount(1);
      catIds.push((await row.getAttribute('data-id'))!);
    }

    const itemNames = Array.from({ length: count }, (_, i) => itemName(tag, i + 1));
    for (let i = 0; i < count; i++) {
      const resp = await page.request.post('/api/catalog/item', {
        form: { name: itemNames[i], price: '150', categoryId: catIds[i] },
      });
      expect(resp.ok(), `create item ${itemNames[i]}`).toBe(true);
    }
    await page.goto('/catalog');
    const itemIds: string[] = [];
    for (const name of itemNames) {
      const row = page.locator(`.catalog-row[data-name="${name}"]`);
      await expect(row, `catalog row for ${name}`).toHaveCount(1);
      itemIds.push((await row.first().getAttribute('data-id'))!);
    }

    for (let i = 0; i < count; i++) {
      const resp = await page.request.post('/api/buttons/add', {
        form: { itemId: itemIds[i], label: itemNames[i], code: itemIds[i] },
      });
      expect(resp.ok(), `add shortcut for ${itemNames[i]}`).toBe(true);
    }

    return catNames.map((name, i) => ({ id: catIds[i], name, itemId: itemIds[i], itemName: itemNames[i] }));
  }

  async function deactivate(page: Page, cats: Cat[]): Promise<void> {
    for (const c of cats) {
      await page.request.post('/api/catalog/item/deactivate', { form: { id: c.itemId } });
    }
  }

  test('(b) only the demo categories (Food, Drinks, Household, Produce): no "..." — identical to before this card (regression)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar).toBeVisible();
    // Requirement 5: hidden entirely, not just visually similar.
    await expect(page.locator('#cat-tab-more')).toBeHidden();
    await expect(page.locator('#cat-tab-more')).toHaveAttribute('hidden', '');

    // No category tab is hidden, and the row itself has nothing to clip —
    // scrollWidth must not exceed clientWidth (the old, now-removed
    // scroll affordance would have made this assertion meaningless; there
    // is no scrollbar left to hide the excess behind any more).
    const overflowPx = await tabBar.evaluate((el) => el.scrollWidth - el.clientWidth);
    expect(overflowPx, 'tab-bar must not overflow its own box').toBeLessThanOrEqual(1);
    const catTabs = tabBar.locator('.tab[data-cat-tab]');
    await expect(catTabs).toHaveCount(4); // Food, Drinks, Household, Produce (see this file's own top comment)
    for (const t of await catTabs.all()) {
      expect(await t.isHidden(), 'no demo category tab should be hidden at 1280px').toBe(false);
    }

    assertClean();
  });

  test('(a) 15 categories at 1280px: strip never scrolls, "..." opens a sheet of every category, selecting the 14th brings it into the strip', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    const cats = await createOverflowCategories(page, 'A', 15);
    try {
      await page.goto('/');

      const tabBar = page.locator('.products .tab-bar');
      await expect(tabBar).toBeVisible();
      const overflowPx = await tabBar.evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(overflowPx, 'tab-bar must never overflow — it is clipped, not scrolled').toBeLessThanOrEqual(1);
      // No horizontal scroll anywhere on the page either.
      const pageOverflowPx = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
      );
      expect(pageOverflowPx, 'page must not scroll horizontally').toBeLessThanOrEqual(1);

      const more = page.locator('#cat-tab-more');
      await expect(more).toBeVisible();
      await expect(more).toHaveAttribute('aria-haspopup', 'dialog');
      await expect(more).toHaveAttribute('aria-expanded', 'false');

      // At least one of the 15 new category tabs must be hidden — that is
      // exactly what makes "..." show at all.
      let hiddenCount = 0;
      for (const c of cats) {
        const tab = page.locator('#cat-tab-' + c.id);
        if (await tab.isHidden()) hiddenCount++;
      }
      expect(hiddenCount, 'at least one category tab must have overflowed').toBeGreaterThan(0);

      // Opening the sheet: every one of the 15 categories is listed, each
      // as its own large tile with a name, colour swatch and quick-button
      // count (ut-docs#2450: this is the button count, not the catalog's
      // real item count — those are two different numbers and now two
      // different i18n keys).
      await more.click();
      const dialog = page.locator('#category-overflow-dialog');
      await expect(dialog).toBeVisible();
      await expect(more).toHaveAttribute('aria-expanded', 'true');
      for (const c of cats) {
        const tile = dialog.locator('.category-overflow-tile', { hasText: c.name });
        await expect(tile, `sheet tile for ${c.name}`).toHaveCount(1);
        await expect(tile.locator('.category-overflow-count')).toHaveText('1 button(s)');
      }

      // Selecting the 14th category (card's own example) closes the sheet
      // and promotes it into the visible strip — the strip never scrolls,
      // so "bring it into view" can only mean "make its own tab visible".
      const fourteenth = cats[13];
      await dialog.locator('.category-overflow-tile', { hasText: fourteenth.name }).click();
      await expect(dialog).toBeHidden();
      await expect(more).toHaveAttribute('aria-expanded', 'false');

      const promotedTab = page.locator('#cat-tab-' + fourteenth.id);
      await expect(promotedTab).toBeVisible();
      await expect(promotedTab).toHaveClass(/active/);
      await expect(promotedTab).toHaveAttribute('aria-selected', 'true');
      const promotedPanel = page.locator('#cat-panel-' + fourteenth.id);
      await expect(promotedPanel.locator('.btn-tile', { hasText: fourteenth.itemName })).toBeVisible();
    } finally {
      await deactivate(page, cats);
    }

    assertClean();
  });

  test('(c) keyboard: roving tabindex reaches only visible tabs; the sheet opens/closes via keyboard and returns focus to "..."', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    const cats = await createOverflowCategories(page, 'C', 15);
    try {
      await page.goto('/');

      const tabBar = page.locator('.products .tab-bar');
      const more = page.locator('#cat-tab-more');
      await expect(more).toBeVisible(); // same overflow precondition as test (a)

      const visibleTabs = tabBar.locator('.tab[role="tab"]:not([hidden])');
      const visibleCount = await visibleTabs.count();
      expect(visibleCount).toBeGreaterThan(1);

      // Arrow-key roving (focusTab, buttons.html) must only ever land on
      // a tab that is actually visible on screen — walk one full lap
      // (plus a couple extra, to also prove the wrap-around stays clean)
      // and check every stop.
      await tabBar.locator('.tab.active').first().focus();
      for (let i = 0; i < visibleCount + 2; i++) {
        const hidden = await page.evaluate(() => (document.activeElement as HTMLElement | null)?.hidden ?? true);
        expect(hidden, `roving-tabindex stop ${i} must be a visible tab`).toBe(false);
        await page.keyboard.press('ArrowRight');
      }

      // The trailing "..." button is a plain Tab stop right after the
      // active tab — it is deliberately NOT part of the tablist's own
      // roving arrow-key set (it opens a dialog, it does not select a
      // panel).
      await tabBar.locator('.tab.active').first().focus();
      await page.keyboard.press('Tab');
      await expect(more).toBeFocused();

      // Opens via keyboard (Enter), focus moves into the sheet, and it is
      // a real focus trap: Tab all the way around never lands outside it.
      await page.keyboard.press('Enter');
      const dialog = page.locator('#category-overflow-dialog');
      await expect(dialog).toBeVisible();
      await expect(more).toHaveAttribute('aria-expanded', 'true');
      const closeBtn = dialog.getByRole('button', { name: 'Close' });
      await expect(closeBtn).toBeFocused();

      const focusableCount = await dialog.locator('button').count();
      for (let i = 0; i < focusableCount + 1; i++) {
        await page.keyboard.press('Tab');
        const withinDialog = await page.evaluate(() => {
          const dlg = document.getElementById('category-overflow-dialog');
          return !!(dlg && document.activeElement && dlg.contains(document.activeElement));
        });
        expect(withinDialog, `tab stop ${i} must stay inside the sheet`).toBe(true);
      }

      // Escape closes it and returns focus to the "..." trigger.
      await page.keyboard.press('Escape');
      await expect(dialog).toBeHidden();
      await expect(more).toBeFocused();
      await expect(more).toHaveAttribute('aria-expanded', 'false');
    } finally {
      await deactivate(page, cats);
    }

    assertClean();
  });

  // Both assertions below are independent-review regressions (ut-docs#2307
  // review), each for a bug reproduced live on the first implementation:
  //
  //  1. Re-widening never brought hidden tabs back. .tab-bar .tab is
  //     flex: 1 1 0% (app.css), so a VISIBLE tab's rendered width is its
  //     stretched share of the row, not the width it needs — and that
  //     share depends on how many siblings applyCategoryOverflow() hid on
  //     its previous pass, so the measurement fed on its own output and
  //     ratcheted one way only. 1600 -> 620 -> 1600 left two tabs showing
  //     where a fresh load at 1600 showed four, permanent until a reload.
  //     The strip is now measured with .ut-measuring (flex-grow off, every
  //     tab un-hidden) so every pass reads real content widths.
  //
  //  2. Narrowing could hide the SELECTED tab, which left the strip with
  //     no active tab while its panel still showed that category's items,
  //     and — because the roving tabindex puts the only tabindex="0" on
  //     the selected tab — left the whole tablist with no keyboard stop at
  //     all. applyCategoryOverflow() now promotes the selected tab into
  //     the fitting set, the same thing selectCategoryFromOverflow()
  //     already did for the sheet path.
  //
  // This is the divider-drag path (ut-docs#2308) the ResizeObserver exists
  // for, exercised through the viewport because that resizes the same box.
  test('(d) resizing the strip: re-widening restores tabs, and the selected tab is never the one hidden (review)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1600, height: 800 });
    const cats = await createOverflowCategories(page, 'D', 15);
    try {
      await page.goto('/');
      const tabBar = page.locator('.products .tab-bar');
      await expect(tabBar).toBeVisible();
      await expect(page.locator('#cat-tab-more')).toBeVisible();

      const visibleCats = tabBar.locator('.tab[data-cat-tab]:not([hidden])');
      const wideCount = await visibleCats.count();
      expect(wideCount, 'some category tabs must fit at 1600px').toBeGreaterThan(1);

      // Select the LAST tab that currently fits — the one a narrowing
      // resize would otherwise hide first.
      await visibleCats.nth(wideCount - 1).click();
      const selectedId = (await tabBar
        .locator('.tab[data-cat-tab].active')
        .getAttribute('id'))!;

      const state = () =>
        page.evaluate(() => {
          const bar = document.querySelector('.products .tab-bar') as HTMLElement;
          const cats2 = Array.from(bar.querySelectorAll('.tab[data-cat-tab]')) as HTMLElement[];
          const active = bar.querySelector('.tab[data-cat-tab].active') as HTMLElement | null;
          const roving = Array.from(bar.querySelectorAll('[role=tab]')) as HTMLElement[];
          return {
            visible: cats2.filter((t) => !t.hidden).length,
            overflowPx: bar.scrollWidth - bar.clientWidth,
            activeId: active?.id ?? null,
            activeHidden: active?.hidden ?? null,
            keyboardStops: roving.filter((t) => !t.hidden && t.getAttribute('tabindex') === '0').length,
          };
        });

      await page.setViewportSize({ width: 620, height: 800 });
      await expect
        .poll(async () => (await state()).visible, { message: 'strip re-fits after narrowing' })
        .toBeLessThan(wideCount);
      const narrow = await state();
      expect(narrow.overflowPx, 'narrowed row must be re-fitted, never clipped').toBeLessThanOrEqual(1);
      expect(narrow.activeId, 'the selected tab must survive the narrowing').toBe(selectedId);
      expect(narrow.activeHidden, 'the selected tab must not be the one hidden').toBe(false);
      expect(narrow.keyboardStops, 'the tablist must keep a keyboard stop').toBeGreaterThan(0);

      // Widening back must restore exactly what this width showed before.
      await page.setViewportSize({ width: 1600, height: 800 });
      await expect
        .poll(async () => (await state()).visible, { message: 're-widening restores the hidden tabs' })
        .toBe(wideCount);
      const wide = await state();
      expect(wide.overflowPx).toBeLessThanOrEqual(1);
      expect(wide.activeHidden, 'the selected tab stays visible after re-widening').toBe(false);
    } finally {
      await deactivate(page, cats);
    }

    assertClean();
  });
});
