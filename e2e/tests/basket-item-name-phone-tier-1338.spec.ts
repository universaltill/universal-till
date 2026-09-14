import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#1338 (follow-up to ut-docs#1314): #1314 fixed the item-name
// column at the kiosk floor (1024x600) and default till (1280x800) but
// left the 360px phone tier with only ~20px (~2 characters) for the name
// -- declared out of scope there and filed as this card. Rather than a
// 5th column-budget shave (app.css's ".basket table" comment carries the
// history of the four already-tried ones), this tier gives each basket
// line a 2-row layout: the name on its own full-width row, qty/price/
// total/remove sharing a second row below (app.css, new
// `@media (max-width: 480px)` block next to the existing #1314 comment).
const CHEDDAR = '5000000000104'; // itm010 "Cheddar Cheese 400g" (001_init.sql + 023 checksum fix)

async function scan(page, code: string) {
  // ut-docs#1284, re-confirmed live at this card's review: `getByRole(
  // 'textbox').first()` used to safely skip the basket's qty-input
  // (type="number" -> role "spinbutton"), but #1284's decimal-corruption
  // fix made it type="text" (role "textbox") -- and the basket precedes
  // the scan row in DOM order, so the moment a line exists `.first()`
  // resolves to `.qty-input`, silently types the BARCODE into the
  // quantity field, and leaves the scan field empty; the `required`
  // scan form then never submits and the wait below hangs the full
  // 30s test timeout. Measured directly: with one line in the basket,
  // `.first().fill('5000000000029')` left `.qty-input` = "5000000000029"
  // and `input[name=code]` = "". sale-screen-213 and
  // basket-no-horizontal-scroll-391 already scope to the scan row's own
  // barcode input for exactly this reason -- do the same here.
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function nameClipState(page) {
  await waitForStableLayout(page, '.basket-scroll, .basket .line-name');
  return page.evaluate(() => {
    const el = document.querySelector('.basket .line-name') as HTMLElement;
    const r = el.getBoundingClientRect();
    return {
      text: el.textContent?.trim(),
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
      renderedWidth: r.width,
    };
  });
}

test.describe('basket item name is legible at the 360px phone tier (ut-docs#1338)', () => {
  test.use({ viewport: { width: 360, height: 640 } });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a real 19-char product name renders in full, not 1-2 characters wide', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);
    await expect(page.locator('.basket .line-name')).toHaveText('Cheddar Cheese 400g');

    const name = await nameClipState(page);
    expect(
      name.scrollHeight,
      `"${name.text}" must not be vertically clamped away (scrollHeight ${name.scrollHeight} vs clientHeight ${name.clientHeight})`,
    ).toBeLessThanOrEqual(name.clientHeight + 1);
    expect(
      name.scrollWidth,
      `"${name.text}" must not be horizontally clipped (scrollWidth ${name.scrollWidth} vs clientWidth ${name.clientWidth})`,
    ).toBeLessThanOrEqual(name.clientWidth + 1);
    // The regression this card exists to fix: ~20px (~2 characters) of
    // rendered width for the name. Assert a real floor, not just "no clip".
    expect(name.renderedWidth, `name column must be genuinely wide, not the old ~20px sliver (was ${name.renderedWidth}px)`).toBeGreaterThan(120);
    assertClean();
  });

  test('a long German compound name wraps readably instead of clipping mid-word', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);
    await page.evaluate(() => {
      (document.querySelector('.basket .line-name') as HTMLElement).textContent =
        'Doppelrahmfrischkäse 200g';
    });

    const name = await nameClipState(page);
    expect(
      name.scrollWidth,
      `"${name.text}" must wrap (overflow-wrap), never clip mid-word horizontally (scrollWidth ${name.scrollWidth} vs clientWidth ${name.clientWidth})`,
    ).toBeLessThanOrEqual(name.clientWidth + 1);
    expect(
      name.scrollHeight,
      `"${name.text}" must fit its line clamp un-truncated (scrollHeight ${name.scrollHeight} vs clientHeight ${name.clientHeight})`,
    ).toBeLessThanOrEqual(name.clientHeight + 1);
    assertClean();
  });

  test('basket-scroll still has no horizontal overflow at 360px (no ut-docs#391 regression)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);

    await waitForStableLayout(page, '.basket-scroll');
    const overflow = await page.evaluate(() => {
      const el = document.querySelector('.basket-scroll') as HTMLElement;
      return { scrollWidth: el.scrollWidth, clientWidth: el.clientWidth };
    });
    expect(
      overflow.scrollWidth,
      `basket-scroll must not need horizontal scroll (scrollWidth ${overflow.scrollWidth} vs clientWidth ${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);
    assertClean();
  });

  test('qty, price and total remain distinct and visible on the line\'s second row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);

    await expect(page.locator('.qty-input').first()).toBeVisible();
    const row = await page.evaluate(() => {
      const tr = document.querySelector('.basket tbody tr') as HTMLElement;
      const qty = tr.querySelector('td:nth-child(2)')!.getBoundingClientRect();
      const price = tr.querySelector('td:nth-child(3)')!.getBoundingClientRect();
      const total = tr.querySelector('td:nth-child(4)')!.getBoundingClientRect();
      const remove = tr.querySelector('td:nth-child(5)')!.getBoundingClientRect();
      return { qty, price, total, remove };
    });
    // The four controls must not overlap each other -- each is its own
    // grid-area column on the line's second row.
    expect(row.qty.right, 'qty must not overlap price').toBeLessThanOrEqual(row.price.left + 1);
    expect(row.price.right, 'price must not overlap total').toBeLessThanOrEqual(row.total.left + 1);
    expect(row.total.right, 'total must not overlap remove').toBeLessThanOrEqual(row.remove.left + 1);
    assertClean();
  });

  // ---- independent review (ut-docs#1338) added the three below ----

  test('the name cell really spans the whole card width, not just the reserved-rem sum', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);
    await waitForStableLayout(page, '.basket-scroll, .basket .line-name');

    // The whole point of this tier is that the name stops being sized by
    // the qty/price/total/remove budget. A `name` area spanning only the
    // four rigid rem tracks is still capped at their sum (measured 272.0px
    // of an available 288.3px at 360px, and 272.0px of 408.3px at 480px --
    // a third of the row unused). The row's leading `1fr` spacer
    // (app.css) is what makes it span the real card width; this asserts
    // that property directly rather than the "> 120px" floor above, which
    // the capped version also passed.
    const span = await page.evaluate(() => {
      const tr = document.querySelector('.basket tbody tr') as HTMLElement;
      const nameCell = tr.querySelector('td:nth-child(1)') as HTMLElement;
      return {
        rowWidth: tr.getBoundingClientRect().width,
        nameCellWidth: nameCell.getBoundingClientRect().width,
      };
    });
    expect(
      span.nameCellWidth,
      `the item cell must span the whole line, not the reserved-column sum (${span.nameCellWidth}px of ${span.rowWidth}px)`,
    ).toBeGreaterThanOrEqual(span.rowWidth - 1);
    assertClean();
  });

  test('the line\'s second row costs no more width than the table\'s own reserved budget', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);
    await waitForStableLayout(page, '.basket-scroll');

    // ut-docs#391's no-horizontal-scroll invariant, at the one corner its
    // own matrix cannot see: that spec sweeps ui_scale 1..2 at 1024px and
    // 901px, both ABOVE this breakpoint, and the basket already overflows
    // at 360px above scale 1 (pre-existing: 132px at 1.5, 294px at 2,
    // measured at review). This tier must therefore not make that WORSE,
    // and the only way it can is by declaring more width than the
    // >=481px table already reserves -- 4.3 + 4 + 4 + 2.8 = 15.1rem. A
    // `column-gap` between the four is exactly such an addition (+.9rem,
    // measured +23px at ui_scale 1.5 / +31px at 2). Direction-agnostic
    // (min-left..max-right) so it holds in RTL too.
    const budget = await page.evaluate(() => {
      const tr = document.querySelector('.basket tbody tr') as HTMLElement;
      const boxes = [2, 3, 4, 5].map((n) => tr.querySelector(`td:nth-child(${n})`)!.getBoundingClientRect());
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
      return {
        span: Math.max(...boxes.map((b) => b.right)) - Math.min(...boxes.map((b) => b.left)),
        reserved: 15.1 * rem,
      };
    });
    expect(
      budget.span,
      `second row must fit the table's own 15.1rem reserved budget (${budget.span.toFixed(1)}px vs ${budget.reserved.toFixed(1)}px) -- anything more is added horizontal overflow at ui_scale > 1`,
    ).toBeLessThanOrEqual(budget.reserved + 1);
    assertClean();
  });

  test('the header row still carries its separator above the first line', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CHEDDAR);
    await waitForStableLayout(page, '.basket-scroll');

    // `display: block` on <thead> makes its single header row a
    // `:last-child` as well, so an un-scoped `.basket tr:last-child {
    // border-bottom: none }` silently erased the header rule at this tier
    // (measured 0px) while still looking plausible in a screenshot.
    const width = await page.evaluate(() => {
      const thr = document.querySelector('.basket thead tr') as HTMLElement;
      return parseFloat(getComputedStyle(thr).borderBottomWidth);
    });
    expect(width, `header row must keep a visible bottom rule (got ${width}px)`).toBeGreaterThanOrEqual(1);
    assertClean();
  });
});
