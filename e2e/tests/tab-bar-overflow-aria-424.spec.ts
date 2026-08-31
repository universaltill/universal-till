import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, waitForStableLayout } from './helpers';

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
    let items: OverflowItem[] = [];
    test.afterEach(async ({ page }) => {
      if (items.length) await cleanupOverflowItems(page, items);
      items = [];
    });

    test('12+ categories wrap the tab bar onto multiple rows instead of squashing labels', async ({ page }) => {
      const assertClean = watchConsole(page);
      items = overflowItems('Wrap', '71');

      await seedOverflowCategories(page, 'import-424-wrap.csv', items);

      await page.goto('/');
      const tabBar = page.locator('.products .tab-bar');
      await expect(tabBar).toBeVisible();
      await expect(tabBar.locator('.tab')).toHaveCount(CATEGORY_COUNT + 2, { timeout: 10_000 }); // + seeded Food/Drinks
      await waitForStableLayout(page, '.products .tab-bar, .products .tab-bar .tab');

      const boxes = await tabBar.locator('.tab').evaluateAll((els) =>
        els.map((el) => {
          const r = (el as HTMLElement).getBoundingClientRect();
          return { top: Math.round(r.top), height: Math.round(r.height) };
        }),
      );

      // Wrapped onto more than one row: more than one distinct top offset
      // among the tabs (ut-docs#424's flex-wrap fix).
      const rowsSeen = new Set(boxes.map((b) => b.top));
      expect(rowsSeen.size, 'tab bar should wrap many categories onto multiple rows').toBeGreaterThan(1);

      // No tab collapsed to a multi-line squashed label — every tab stays
      // a single readable line (well under a two-line height at this UI's
      // fluid font-size/line-height).
      for (const b of boxes) {
        expect(b.height, 'a tab must not have wrapped its own label across multiple lines').toBeLessThan(60);
      }

      assertClean();
    });

    test('category tabs carry the full WAI-ARIA tabs pattern and respond to arrow keys', async ({ page }) => {
      const assertClean = watchConsole(page);
      items = overflowItems('Aria', '72');

      await seedOverflowCategories(page, 'import-424-aria.csv', items);

      await page.goto('/');
      const tabBar = page.locator('.products .tab-bar');
      const tabs = tabBar.getByRole('tab');
      await expect(tabs).toHaveCount(CATEGORY_COUNT + 2, { timeout: 10_000 });
      const count = await tabs.count();

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
  });
});
