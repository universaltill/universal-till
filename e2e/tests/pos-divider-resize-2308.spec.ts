import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2308: "I want the user to be able to tap and hold the line
// between the basket and the quick items, and shift it left and right to
// make each side larger or smaller." — the basket/products divider on the
// sell screen (web/ui/pages/index.html, app.css's `.pos-container`).
//
// Viewport: 1280x800 throughout (except the below-breakpoint test, which
// deliberately overrides it), matching the pilot tablet's real resolution
// (10.1", 1280x800) named in this card's own acceptance criteria. Real
// touch hardware was not available in this session — the touch/hold specs
// below dispatch a synthetic PointerEvent sequence with pointerType:
// 'touch' via Playwright's dispatchEvent, the same technique and the same
// honesty caveat as tables-touch-drag-1170.spec.ts: this exercises the
// app's own pointerdown/pointermove/pointerup handling exactly as a real
// touchscreen drag would reach it, but it cannot rule out an OS/compositor-
// level event-delivery problem — physical-device verification on the pilot
// tablet is tracked as a separate manual follow-up, not claimed done here.
test.use({ viewport: { width: 1280, height: 800 } });

function dividerLocator(page) {
  return page.getByTestId('pos-divider');
}

// Both panes are `hx-trigger="load" hx-swap="outerHTML"` placeholders in
// index.html — an empty, zero-height div until /ui/basket and /ui/buttons
// swap the real fragment in. `page.goto` resolves on `load`, which is also
// what fires those swaps, so measuring straight after it can land on the
// placeholder, whose boundingBox() is null (seen live under 4 parallel
// workers, ut-docs#2345). Wait for the swapped-in fragment itself: only
// the placeholders carry `hx-trigger="load"` (the real fragments re-trigger
// on their own body events instead), so that attribute's absence is a
// content-independent "swapped in" marker — the basket partial's
// `id="basket"` would do too, but the buttons partial has no such id and
// its `.products-header` only renders for an EMPTY catalog.
const SWAPPED_IN = ':not([hx-trigger="load"])';

async function basketWidth(page) {
  const basket = page.locator(`.pos-container > .basket${SWAPPED_IN}`);
  await expect(basket).toBeVisible();
  const box = await basket.boundingBox();
  expect(box, '.basket must have a measurable box').toBeTruthy();
  return box!.width;
}

async function productsWidth(page) {
  const products = page.locator(`.pos-container > .products${SWAPPED_IN}`);
  await expect(products).toBeVisible();
  const box = await products.boundingBox();
  expect(box, '.products must have a measurable box').toBeTruthy();
  return box!.width;
}

async function resetDividerSetting(page) {
  // Shared till (every file in this worker drives the same server, see
  // playwright.config.ts) — every mutating test here must leave the
  // persisted setting exactly as it found it (unset/default), same
  // "restore default so later specs sharing this server aren't affected"
  // discipline as ui-scale-basket.spec.ts.
  await page.request.post('/api/settings/basket-panel-width', { form: { width_rem: '0' } });
}

test.describe('sell-screen basket/products divider (ut-docs#2308)', () => {
  test.afterEach(async ({ page }) => {
    await resetDividerSetting(page);
    await page.request.post('/api/pos/reset');
  });

  test('mouse drag resizes both panes live and clamps at the limits', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();

    const startBasket = await basketWidth(page);
    const startProducts = await productsWidth(page);

    const box = (await divider.boundingBox())!;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;

    // Plain drag for mouse — no hold required.
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 100, cy, { steps: 10 });
    await page.mouse.up();

    const grownBasket = await basketWidth(page);
    const grownProducts = await productsWidth(page);
    expect(grownBasket, 'basket should have grown').toBeGreaterThan(startBasket + 50);
    expect(grownProducts, 'products should have shrunk').toBeLessThan(startProducts - 50);

    // Drag far past the container's own width toward the basket side —
    // the products pane must never collapse below its own usable minimum
    // (two tiles' worth, app.css's --pos-products-min-w).
    await page.mouse.move(cx + 100, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 100 + 2000, cy, { steps: 5 });
    await page.mouse.up();
    const clampedProducts = await productsWidth(page);
    // 16.5rem at a 16px root is 264px; a healthy margin above that (still
    // well short of "collapsed") proves the clamp held, not a precise
    // pixel assertion of the exact floor.
    expect(clampedProducts, 'products pane must never collapse').toBeGreaterThan(150);

    // Drag far the other way — the basket pane must never collapse below
    // its own usable minimum (name+qty+price columns, --pos-basket-min-w).
    const box2 = (await divider.boundingBox())!;
    const cx2 = box2.x + box2.width / 2;
    await page.mouse.move(cx2, cy);
    await page.mouse.down();
    await page.mouse.move(cx2 - 2000, cy, { steps: 5 });
    await page.mouse.up();
    const clampedBasket = await basketWidth(page);
    expect(clampedBasket, 'basket pane must never collapse').toBeGreaterThan(200);

    assertClean();
  });

  test('a tap on a tile immediately adjacent to the divider still adds to the basket', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await expect(dividerLocator(page)).toBeVisible();

    // The first tile in the products grid renders immediately against the
    // divider's own edge (the grid's first cell, top-left) — exactly the
    // "tile immediately adjacent to the divider" this acceptance
    // criterion names. The divider is its own separate, thin grid track
    // (app.css `.pos-container`), so a real tap here must reach the tile
    // untouched, not be swallowed by the divider's own pointer handling.
    const firstTile = page.locator('.products .btn-tile').first();
    await expect(firstTile).toBeVisible();
    const tileBox = await firstTile.boundingBox();
    const dividerBox = await dividerLocator(page).boundingBox();
    expect(tileBox && dividerBox, 'both boxes must be measurable').toBeTruthy();
    expect(
      tileBox!.x - (dividerBox!.x + dividerBox!.width),
      'first tile should render right up against the divider, not floating away from it',
    ).toBeLessThan(40);

    await expect(page.locator('.basket .basket-count')).toHaveText('0');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      firstTile.click(),
    ]);
    await expect(page.locator('.basket table tbody tr')).toHaveCount(1);

    assertClean();
  });

  test('the chosen ratio persists across a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();

    const before = await basketWidth(page);
    const box = (await divider.boundingBox())!;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;

    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/basket-panel-width')),
      (async () => {
        await page.mouse.move(cx + 120, cy, { steps: 10 });
        await page.mouse.up();
      })(),
    ]);

    const afterDrag = await basketWidth(page);
    expect(afterDrag).toBeGreaterThan(before + 50);

    await page.reload();
    const afterReload = await basketWidth(page);
    // Reload must render the SAME width the drag left it at, not the
    // pre-drag default — within a couple of px of rounding/rem-to-px
    // conversion, not an exact pixel match.
    expect(Math.abs(afterReload - afterDrag), `afterDrag=${afterDrag} afterReload=${afterReload}`).toBeLessThan(5);

    assertClean();
  });

  test('Settings -> Display Reset restores the default split', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();
    const defaultWidth = await basketWidth(page);

    const box = (await divider.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/basket-panel-width')),
      (async () => {
        await page.mouse.move(box.x + box.width / 2 + 120, box.y + box.height / 2, { steps: 10 });
        await page.mouse.up();
      })(),
    ]);
    const dragged = await basketWidth(page);
    expect(dragged).toBeGreaterThan(defaultWidth + 50);

    await page.goto('/settings#settings-display');
    const resetBtn = page.getByTestId('basket-panel-reset');
    await expect(resetBtn).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/basket-panel-width')),
      resetBtn.click(),
    ]);
    await page.waitForEvent('load');

    await page.goto('/');
    const restored = await basketWidth(page);
    expect(Math.abs(restored - defaultWidth), `restored=${restored} default=${defaultWidth}`).toBeLessThan(5);

    assertClean();
  });

  test('double-click on the grip resets to the default split without a reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();
    const defaultWidth = await basketWidth(page);

    const box = (await divider.boundingBox())!;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 120, cy, { steps: 10 });
    await page.mouse.up();
    const dragged = await basketWidth(page);
    expect(dragged).toBeGreaterThan(defaultWidth + 50);

    const navigations: string[] = [];
    page.on('framenavigated', (f) => navigations.push(f.url()));

    const newBox = (await divider.boundingBox())!;
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/basket-panel-width')),
      page.mouse.dblclick(newBox.x + newBox.width / 2, newBox.y + newBox.height / 2),
    ]);

    const afterReset = await basketWidth(page);
    expect(Math.abs(afterReset - defaultWidth), `afterReset=${afterReset} default=${defaultWidth}`).toBeLessThan(5);
    // The whole point of a plain fetch() POST (not htmx/a form) here is
    // that an operator mid-sale never loses their in-progress basket to a
    // page reload — confirm no navigation actually happened.
    expect(navigations, 'double-click reset must not navigate/reload the page').toHaveLength(0);

    assertClean();
  });

  test('drag direction is visually consistent under RTL', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();

    // Same swapped-in-fragment wait as basketWidth/productsWidth above —
    // here the x positions are what's compared, so read the boxes directly.
    await basketWidth(page);
    await productsWidth(page);
    const basketBox = await page.locator(`.pos-container > .basket${SWAPPED_IN}`).boundingBox();
    const productsBox = await page.locator(`.pos-container > .products${SWAPPED_IN}`).boundingBox();
    expect(
      basketBox!.x,
      'basket (DOM-first) should render visually to the RIGHT under RTL, same mirroring this page\'s own focusTab() RTL handling documents',
    ).toBeGreaterThan(productsBox!.x);

    const before = await basketWidth(page);
    const box = (await divider.boundingBox())!;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;

    // The divider always tracks the cursor: dragging it toward the visual
    // RIGHT moves it CLOSER to the container's right edge. Under RTL,
    // basket occupies the space between the divider and that right edge
    // (basket sits on the right here, confirmed above) -- so moving the
    // divider right SHRINKS that space, i.e. shrinks basket. That's the
    // OPPOSITE effect the LTR test above gets from the exact same
    // physical rightward drag (there, basket sits on the LEFT, so the
    // divider moving right GROWS it) -- this opposite-effect-from-the-
    // same-gesture is exactly the "sign flips under RTL" behaviour this
    // acceptance criterion asks for, and falls out of one single
    // mechanism (isBasketVisuallyFirst()'s runtime measurement, index.html)
    // rather than a separate RTL-specific code path.
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 100, cy, { steps: 10 });
    await page.mouse.up();
    const afterRight = await basketWidth(page);
    expect(afterRight, 'dragging toward the visual-right should SHRINK basket under RTL (opposite of the LTR case)').toBeLessThan(before - 50);

    const box2 = (await divider.boundingBox())!;
    await page.mouse.move(box2.x + box2.width / 2, cy);
    await page.mouse.down();
    await page.mouse.move(box2.x + box2.width / 2 - 200, cy, { steps: 10 });
    await page.mouse.up();
    const afterLeft = await basketWidth(page);
    expect(afterLeft, 'dragging back toward the visual-left should GROW basket under RTL').toBeGreaterThan(afterRight + 50);

    assertClean();
  });

  test('touch-and-hold then drag resizes the divider; a plain touch tap does not', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const divider = dividerLocator(page);
    await expect(divider).toBeVisible();

    // Plain touch tap (no hold, no movement): must NOT resize -- a swipe
    // or an accidental brush against the divider's own hit area must
    // never move it.
    const beforeTap = await basketWidth(page);
    const tapBox = (await divider.boundingBox())!;
    const tapX = tapBox.x + tapBox.width / 2, tapY = tapBox.y + tapBox.height / 2;
    await divider.dispatchEvent('pointerdown', {
      pointerId: 7, pointerType: 'touch', isPrimary: true, button: 0, clientX: tapX, clientY: tapY,
    });
    await divider.dispatchEvent('pointerup', {
      pointerId: 7, pointerType: 'touch', clientX: tapX, clientY: tapY,
    });
    const afterTap = await basketWidth(page);
    expect(afterTap, 'a plain tap must not resize the divider').toBe(beforeTap);

    // Touch-and-hold (>=300ms) THEN drag: must resize.
    await divider.dispatchEvent('pointerdown', {
      pointerId: 8, pointerType: 'touch', isPrimary: true, button: 0, clientX: tapX, clientY: tapY,
    });
    await page.waitForTimeout(350); // clears the ~300ms hold threshold
    await divider.dispatchEvent('pointermove', {
      pointerId: 8, pointerType: 'touch', clientX: tapX + 100, clientY: tapY,
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/basket-panel-width')),
      divider.dispatchEvent('pointerup', { pointerId: 8, pointerType: 'touch', clientX: tapX + 100, clientY: tapY }),
    ]);
    const afterHoldDrag = await basketWidth(page);
    expect(afterHoldDrag, 'a touch-and-hold then drag must resize the divider').toBeGreaterThan(beforeTap + 50);

    assertClean();
  });

  test.describe('below the 900px stack breakpoint', () => {
    test.use({ viewport: { width: 800, height: 900 } });

    test('the divider is inactive, not rendered as interactive', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto('/');
      await expect(page.locator('.pos-container')).toBeVisible();
      const divider = dividerLocator(page);
      // `.pos-container` itself stacks to a single column at this width
      // (app.css) -- the divider has no grid cell to occupy there at all
      // and app.css hides it outright; `toBeHidden()` covers both a
      // `display: none` element and one genuinely absent from layout.
      await expect(divider).toBeHidden();

      assertClean();
    });
  });
});
