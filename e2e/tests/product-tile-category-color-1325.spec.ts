import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1325: product tiles show their own category's color as an
// accent, reusing the --cat-color custom property .category-header/.tab
// already carry (app.css). Structural coverage of the actual bug this
// fixes — a top-level category's OWN tiles (not a nested subcategory's)
// render inside its tab panel via "category-group-body", a sibling of the
// tab button rather than its descendant, so without the panel itself also
// carrying --cat-color those tiles had NO color ancestor at all and
// silently fell back to --accent for every top-level category — lives in
// internal/ui/buttons_http_test.go
// (TestButtonsHTTPList_TopLevelTabPanelCarriesColorForDirectTiles), which
// pins the server-rendered markup. This spec drives the real browser to
// confirm the CSS actually resolves the way that markup implies: real
// custom-property inheritance, real cascade/specificity against the
// pre-existing 1px .btn-tile border, not just "the right string is
// somewhere in the HTML."
//
// Drives the real demo-seeded catalog (001_init.sql): Coca-Cola 330ml
// (itm001) sits directly under the top-level "Drinks" category (cat_drink)
// with no subcategory nesting — exactly the render path
// TopLevelTabPanelCarriesColorForDirectTiles pins, and the same seeded tile
// sale-screen-category-tabs-search-418.spec.ts already relies on.
test.describe('product tile category-color accent (ut-docs#1325)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a top-level category tile inherits its tab panel\'s --cat-color, not the fallback accent', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const tabBar = page.locator('.products .tab-bar');
    await tabBar.getByRole('tab', { name: 'Drinks' }).click();

    const colaTile = page.locator('.btn-tile', { hasText: 'Coca-Cola 330ml' });
    await expect(colaTile).toBeVisible();

    const { panelVar, tileBorder, accentBorder } = await page.evaluate(() => {
      // Resolve a CSS color string to the browser's own normalized rgb()
      // form via a scratch element, so a raw custom-property value (e.g.
      // "#2563EB") can be compared against a computed border color (always
      // returned as "rgb(...)") without hand-rolling hex parsing.
      const toRgb = (colorStr: string) => {
        const d = document.createElement('div');
        d.style.color = colorStr;
        document.body.appendChild(d);
        const rgb = getComputedStyle(d).color;
        d.remove();
        return rgb;
      };
      const panel = document.getElementById('cat-panel-cat_drink');
      const tile = Array.from(document.querySelectorAll('.btn-tile')).find((el) =>
        el.textContent?.includes('Coca-Cola 330ml'),
      );
      const panelColorVar = panel ? getComputedStyle(panel).getPropertyValue('--cat-color').trim() : '';
      const accentVar = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
      return {
        panelVar: toRgb(panelColorVar),
        tileBorder: tile ? getComputedStyle(tile).borderInlineStartColor : '',
        accentBorder: toRgb(accentVar),
      };
    });

    // The tile's real rendered border-inline-start color must resolve to
    // the SAME color as its tab panel's --cat-color (real inheritance
    // working end to end), and must NOT have silently fallen back to the
    // generic --accent — the exact regression this ticket fixes.
    expect(panelVar).not.toBe('');
    expect(tileBorder).toBe(panelVar);
    expect(tileBorder).not.toBe(accentBorder);

    assertClean();
  });
});
