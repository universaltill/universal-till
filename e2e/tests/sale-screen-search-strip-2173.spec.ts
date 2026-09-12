import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2173 (closes ut-docs#993): the sale screen's category tab bar and
// its search input used to occupy two separate full-height rows
// (.products-header, then .tab-bar) above the product tiles. This spec
// covers the replacement single-row .products-strip: the category tab bar
// stays visible by default with the search/edit icon buttons pinned at its
// trailing end, and tapping the search icon swaps the strip out for
// #products-search IN PLACE (same row, same height) with a back arrow at
// the leading end restoring it. The ut-docs#993 "a tile row is actually
// visible at 1024x600" geometry assertion itself lives in
// tab-bar-overflow-aria-424.spec.ts (it's this same tab bar, and that file
// already owns its overflow/WAI-ARIA geometry coverage) — this file is
// scoped to the search-strip interaction the strip itself adds.

const CATEGORY_COUNT = 12;

type OverflowItem = { name: string; sku: string; barcode: string; category: string };

// Copied from tab-bar-overflow-aria-424.spec.ts (not exported there) —
// same shape, same "disjoint tag/barcodePrefix" rule so catalog import's
// upsert-by-SKU/barcode never lets two specs collide on the same rows. Used
// here only by the scroll/RTL tests below, which need enough categories to
// force real horizontal overflow the same way that file's own tests do.
function overflowItems(tag: string, barcodePrefix: string): OverflowItem[] {
  return Array.from({ length: CATEGORY_COUNT }, (_, i) => {
    const n = String(i + 1).padStart(2, '0');
    return {
      name: `Strip2173 ${tag} Item ${n}`,
      sku: `STRIP2173${tag}${n}`,
      barcode: `${barcodePrefix}${n}`,
      category: `Strip2173 ${tag} Cat ${n}`,
    };
  });
}

function overflowCsv(items: OverflowItem[]): string {
  const rows = items.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${it.category},1`).join('\n');
  return 'Name,SKU,Barcode,Price,Category,In stock\n' + rows;
}

async function seedOverflowCategories(page: Page, fileName: string, items: OverflowItem[]) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: fileName,
    mimeType: 'text/csv',
    buffer: Buffer.from(overflowCsv(items)),
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

async function cleanupOverflowItems(page: Page, items: OverflowItem[]) {
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

test.describe('sale-screen category strip: search expand/collapse (ut-docs#2173)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('at rest: the category strip and both icon buttons show, the search input does not', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    await expect(page.locator('.products .tab-bar')).toBeVisible();
    await expect(page.locator('.products-strip-search')).toBeVisible();
    await expect(page.locator('.products-strip-edit')).toBeVisible();
    await expect(page.locator('#products-search')).toBeHidden();
    await expect(page.locator('.products-strip-back')).toBeHidden();

    assertClean();
  });

  test('both icon controls carry a non-empty aria-label and title (ut-docs#2010 standard)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    for (const selector of ['.products-strip-search', '.products-strip-edit']) {
      const el = page.locator(selector);
      await expect(el).toBeVisible();
      expect(await el.getAttribute('aria-label'), `${selector} aria-label`).toBeTruthy();
      expect(await el.getAttribute('title'), `${selector} title`).toBeTruthy();
    }

    // The back arrow only exists in the accessibility tree meaningfully
    // once search is open, but it must carry the same pair the moment it
    // shows.
    await page.locator('.products-strip-search').click();
    const back = page.locator('.products-strip-back');
    await expect(back).toBeVisible();
    expect(await back.getAttribute('aria-label'), 'back button aria-label').toBeTruthy();
    expect(await back.getAttribute('title'), 'back button title').toBeTruthy();

    assertClean();
  });

  test('tapping search opens the input in place, without growing the row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const strip = page.locator('.products-strip');
    const beforeBox = await strip.boundingBox();
    expect(beforeBox, 'strip must have a measurable box before opening search').toBeTruthy();

    await page.locator('.products-strip-search').click();

    const search = page.locator('#products-search');
    await expect(search).toBeVisible();
    await expect(search).toBeFocused();
    await expect(page.locator('.products .tab-bar')).toBeHidden();
    await expect(page.locator('.products-strip-search')).toBeHidden();
    await expect(page.locator('.products-strip-edit')).toBeHidden();
    await expect(page.locator('.products-strip-back')).toBeVisible();

    // ut-docs#2173's whole point: search swaps IN PLACE, same row, same
    // height — never a second row.
    const afterBox = await strip.boundingBox();
    expect(afterBox, 'strip must have a measurable box after opening search').toBeTruthy();
    expect(
      Math.abs(afterBox!.height - beforeBox!.height),
      `strip height should not change opening search (before ${beforeBox!.height}px, after ${afterBox!.height}px)`,
    ).toBeLessThan(1);

    assertClean();
  });

  test('typing a query filters tiles the same way it always has', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    // Food is the default-active tab (sale-screen-category-tabs-search-418.spec.ts).
    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill('Butter');

    await expect(page.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeHidden();

    assertClean();
  });

  test('the back arrow restores the strip, clears the query, and keeps the same category selected', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const activeTabId = await page.locator('.products .tab-bar .tab.active').getAttribute('id');
    expect(activeTabId, 'an active tab id must exist before opening search').toBeTruthy();

    await page.locator('.products-strip-search').click();
    await page.locator('#products-search').fill('Butter');
    await expect(page.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();

    await page.locator('.products-strip-back').click();

    const search = page.locator('#products-search');
    await expect(search).toBeHidden();
    await expect(search).toHaveValue('');
    await expect(page.locator('.products .tab-bar')).toBeVisible();
    await expect(page.locator('.products-strip-search')).toBeVisible();
    await expect(page.locator('.products-strip-edit')).toBeVisible();
    await expect(page.locator('.products-strip-back')).toBeHidden();

    // Same category, still selected — closeSearch() only resets the search
    // state, never the active tab.
    await expect(page.locator(`#${activeTabId}`)).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeVisible();

    // ut-docs#2173 review finding F2: closing search hides the element that
    // had focus, so closeSearch() must hand focus back rather than drop it.
    // Without this, a keyboard or screen-reader operator lands on <body> and
    // has to Tab in from the top of the document again — on the till's
    // primary screen. Reproduced during review before the fix.
    await expect(page.locator('.products-strip-search'), 'focus must return to the search icon, not fall to <body>').toBeFocused();

    assertClean();
  });

  test('Escape from the expanded input collapses it the same way the back arrow does', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const activeTabId = await page.locator('.products .tab-bar .tab.active').getAttribute('id');

    await page.locator('.products-strip-search').click();
    const search = page.locator('#products-search');
    await search.fill('Butter');
    await search.press('Escape');

    await expect(search).toBeHidden();
    await expect(search).toHaveValue('');
    await expect(page.locator('.products .tab-bar')).toBeVisible();
    await expect(page.locator('.products-strip-back')).toBeHidden();
    await expect(page.locator(`#${activeTabId}`)).toHaveAttribute('aria-selected', 'true');
    // Review F2, same as the back-arrow path above.
    await expect(page.locator('.products-strip-search'), 'focus must return to the search icon, not fall to <body>').toBeFocused();

    // Escape must also work when focus sits on the back button rather than
    // in the input — it is the other reachable element in search mode, and
    // before review F2 it had no Escape handler at all.
    await page.locator('.products-strip-search').click();
    await expect(search).toBeVisible();
    await page.locator('.products-strip-back').focus();
    await page.locator('.products-strip-back').press('Escape');
    await expect(search).toBeHidden();
    await expect(page.locator('.products .tab-bar')).toBeVisible();

    assertClean();
  });

  // ut-docs#2173 review finding F1: the strip work deleted .products-header /
  // .products-add from app.css, but buttons.html's EMPTY-CATALOG branch
  // ({{ if not .Groups }}) still renders both classes — so a brand-new shop's
  // first screen silently regressed from one ~42px flex row to a 77.8px
  // stacked block, the exact opposite of this card's goal, with no test
  // covering it.
  //
  // What this test pins, stated honestly: the CSS CONTRACT those two classes
  // depend on, by rendering the empty-state markup into the live sale screen
  // and measuring it. It deliberately does NOT empty the real catalog to
  // reach that branch for real — the shortcut buttons are shared, mutable
  // state on this till, and a spec that removed them all and died before
  // restoring would leave every later spec running against an empty sale
  // screen (the same class of cross-spec leak drainParkedOrders exists for).
  // So this catches the regression that actually happened — the rules being
  // deleted while markup still uses them — but not a future change to the
  // markup itself.
  test('the empty-catalog header rules still lay title and add-link out on one row (review F1)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await expect(page.locator('.products-finder')).toBeVisible();

    const box = await page.evaluate(() => {
      // Same markup buttons.html's empty-catalog branch renders.
      const host = document.createElement('div');
      host.innerHTML =
        '<div class="products-header" id="f1-probe">' +
        '<h2>Products</h2>' +
        '<a class="btn primary compact products-add" href="/designer">+ Add product</a>' +
        '</div>';
      const el = host.firstElementChild as HTMLElement;
      document.querySelector('.products')!.appendChild(el);
      const h2 = el.querySelector('h2') as HTMLElement;
      const link = el.querySelector('.products-add') as HTMLElement;
      const r = { display: getComputedStyle(el).display, h2Margin: getComputedStyle(h2).marginBottom,
                  headerHeight: el.getBoundingClientRect().height,
                  h2Top: Math.round(h2.getBoundingClientRect().top),
                  linkTop: Math.round(link.getBoundingClientRect().top) };
      el.remove();
      return r;
    });

    expect(box.display, '.products-header must stay a flex row').toBe('flex');
    expect(box.h2Margin, 'its <h2> keeps the margin reset — the flex gap provides the spacing').toBe('0px');
    // The real signature of the regression: title and add-link stacked onto
    // separate rows, roughly doubling the header's height.
    expect(Math.abs(box.h2Top - box.linkTop), `title and add-link must share one row (h2 top ${box.h2Top}, link top ${box.linkTop})`).toBeLessThan(20);
    expect(box.headerHeight, `empty-state header must stay one compact row, got ${box.headerHeight}px`).toBeLessThan(60);

    assertClean();
  });

  test('keyboard Tab reaches the search and edit controls', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    // ut-docs#2173 review finding F3: this test used to call
    // locator.focus() + expect(...).toBeFocused() and claim that proved
    // tab-order membership. It does not — DOM element.focus() succeeds on a
    // tabindex="-1" element, so the assertion passed with BOTH controls
    // pulled out of the tab order (reproduced: set tabindex="-1" on each and
    // the old test still went green). It also never pressed Tab, despite its
    // name. Two assertions that CAN fail replace it:
    //
    //   1. the attribute check ut-docs#1702's spec actually uses, and
    //   2. a real Tab press moving focus from one control to the next —
    //      they are adjacent in DOM order, so this proves the sequential
    //      navigation order genuinely traverses both, without counting Tab
    //      presses from an arbitrary starting point elsewhere on the page.
    const searchBtn = page.locator('.products-strip-search');
    const editLink = page.locator('.products-strip-edit');

    await expect(searchBtn).not.toHaveAttribute('tabindex', '-1');
    await expect(editLink).not.toHaveAttribute('tabindex', '-1');

    await searchBtn.focus();
    await expect(searchBtn).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(editLink, 'Tab from the search icon must land on the edit link — both in the tab order, in strip order').toBeFocused();

    assertClean();
  });
});

test.describe('sale-screen category strip: scrolling + RTL mirroring (ut-docs#2173)', () => {
  let items: OverflowItem[] = [];
  test.afterEach(async ({ page }) => {
    if (items.length) await cleanupOverflowItems(page, items);
    items = [];
    await page.request.post('/api/pos/reset');
  });

  test('LTR: with many categories the strip scrolls horizontally, and the back arrow is NOT mirrored', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    items = overflowItems('Ltr', '76');
    await seedOverflowCategories(page, 'import-2173-ltr.csv', items);

    await page.goto('/');
    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar.locator('.tab')).toHaveCount(CATEGORY_COUNT + 2, { timeout: 10_000 });

    const { scrollWidth, clientWidth } = await tabBar.evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(scrollWidth, `strip should overflow horizontally with ${CATEGORY_COUNT + 2} tabs (scrollWidth ${scrollWidth}, clientWidth ${clientWidth})`).toBeGreaterThan(clientWidth);

    // ut-docs#2173 / ut-docs#2024: the scroll-shadow is the ONLY thing on
    // screen saying more categories exist past the edge — without it a shop
    // with 12 categories sees two tabs and no hint there are ten more, which
    // is the exact gap #2024 was filed for on the catalog strip. app.css
    // ports that recipe here and index.html wires window.utTabBarFade to
    // drive it; assert the user-visible outcome (the ::after pseudo-element's
    // computed opacity), not just the class, so a broken class-to-pseudo
    // wiring fails too. Polled, because the fade cross-fades over app.css's
    // own `transition: opacity .15s ease` and an immediate read can legitimately
    // catch it mid-transition.
    await expect
      .poll(async () => tabBar.evaluate((el) => getComputedStyle(el, '::after').opacity), {
        message: 'end-fade must show while tabs sit off the trailing edge',
      })
      .toBe('1');
    await expect
      .poll(async () => tabBar.evaluate((el) => getComputedStyle(el, '::before').opacity), {
        message: 'no start-fade while the strip is scrolled to the true start',
      })
      .toBe('0');

    // Scroll to the far end: the fades must swap over.
    await tabBar.evaluate((el) => { el.scrollLeft = el.scrollWidth; });
    await expect
      .poll(async () => tabBar.evaluate((el) => getComputedStyle(el, '::before').opacity), {
        message: 'start-fade must appear once scrolled away from the start',
      })
      .toBe('1');
    await expect
      .poll(async () => tabBar.evaluate((el) => getComputedStyle(el, '::after').opacity), {
        message: 'no end-fade at the true end',
      })
      .toBe('0');

    await page.locator('.products-strip-search').click();
    // Wait for the back button to actually be visible (Alpine's x-show DOM
    // write, like the app's own afterTabPaint() note elsewhere documents,
    // is not necessarily synchronous with the click) before reading its
    // computed style, so this can't race a still-mid-toggle DOM.
    await expect(page.locator('.products-strip-back')).toBeVisible();
    const transform = await page.locator('.products-strip-back .btn-ico svg').evaluate((el) => getComputedStyle(el).transform);
    // LTR: no mirroring — either the identity matrix or 'none'.
    expect(transform === 'none' || /matrix\(1, 0, 0, 1/.test(transform), `LTR back arrow should not be mirrored, got transform: ${transform}`).toBe(true);

    assertClean();
  });

  test('RTL (fa): the strip still scrolls, and the back arrow is mirrored', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    items = overflowItems('Rtl', '77');
    await seedOverflowCategories(page, 'import-2173-rtl.csv', items);

    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

    const tabBar = page.locator('.products .tab-bar');
    await expect(tabBar.locator('.tab')).toHaveCount(CATEGORY_COUNT + 2, { timeout: 10_000 });

    const { scrollWidth, clientWidth } = await tabBar.evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(scrollWidth, `strip should overflow horizontally under RTL too (scrollWidth ${scrollWidth}, clientWidth ${clientWidth})`).toBeGreaterThan(clientWidth);

    // RTL's scrollLeft convention (0 → negative) — same Math.abs() handling
    // window.utTabBarFade/app.css rely on elsewhere; scroll to the reading
    // end and confirm it actually moved.
    const scrolledLeft = await tabBar.evaluate((el) => {
      el.scrollLeft = -(el.scrollWidth - el.clientWidth);
      return el.scrollLeft;
    });
    expect(scrolledLeft, 'RTL scrollLeft must actually go negative here').toBeLessThan(0);

    await page.locator('.products-strip-search').click();
    // Same "wait for the real DOM write" reasoning as the LTR test above.
    await expect(page.locator('.products-strip-back')).toBeVisible();
    // getComputedStyle().transform resolves a CSS `transform: scaleX(-1)`
    // to a 2D matrix with a negative x-scale component: matrix(-1, 0, 0, 1, 0, 0).
    const transform = await page.locator('.products-strip-back .btn-ico svg').evaluate((el) => getComputedStyle(el).transform);
    expect(/matrix\(-1, 0, 0, 1/.test(transform), `RTL back arrow should be horizontally flipped, got transform: ${transform}`).toBe(true);

    assertClean();
  });
});
