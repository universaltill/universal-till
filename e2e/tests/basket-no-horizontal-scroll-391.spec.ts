import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#391: third reported recurrence of the basket scrolling
// horizontally with the remove (✕) button clipped in half at the right
// edge. Prior related cards: #213 (basket line visibility), #251 (.fee-row
// clipping on a narrow viewport), #320 (flaky e2e only seeing 3/4 lines).
//
// Root cause: `.basket-scroll` sets `overflow-y: auto` but leaves
// `overflow-x` unset. Per the CSS overflow computed-value rule ("if one of
// 'overflow-x'/'overflow-y' is 'visible' and the other isn't, the used
// value of 'visible' becomes 'auto'"), the browser silently turns the
// unset overflow-x into `auto` too -- so once the basket table's width
// exceeds the panel, a horizontal scrollbar appears and the default scroll
// position (0) shows the table's LEFT edge, clipping the right-most column
// (the remove button) in half.
//
// The real fix (app.css, ".basket table"'s own comment has the full history
// of three attempts, two of which failed independent review or broke
// ut-docs#213's own regression test) reserves exact rem-based space for the
// fixed-content columns (qty+discount inputs, remove button -- these track
// --ui-scale exactly) and leaves the item column with no declared width so
// table-layout: fixed's own algorithm gives it 100% of whatever's left --
// never a guessed ratio, never calc() (table column sizing silently
// mis-evaluates calc(), see that comment). NOT `overflow-x: hidden`, which
// would trade "clipped half the time" for "clipped every time" -- exactly
// what the AC rules out.
//
// ui_scale is in scope, not just the base viewport: independent review of
// a first attempt found it passing the one viewport this file originally
// covered by 0.48px while failing outright (button clipped in half again)
// at ui_scale 2 -- offered in Settings, guarded as supported by
// ui-scale-basket.spec.ts. So this file checks the two documented-floor
// viewports (1024x600 kiosk; 901x600, one px above the mobile breakpoint,
// the narrowest width still using the desktop two-column grid) across
// every scale step Settings actually offers.
const CODES = ['5000000000012', '5000000000029', '5000000000036'];
const SCALES = ['1', '1.15', '1.3', '1.5', '1.75', '2'];
const VIEWPORTS = [
  { width: 1024, height: 600, label: 'kiosk floor (7" 1024x600, ut-docs/hardware/diy-pos.md)' },
  { width: 901, height: 600, label: 'narrowest desktop-grid width (one px above the 900px mobile breakpoint)' },
];

async function scan(page, code: string) {
  await page.getByRole('textbox').first().fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function setScale(page, scale: string) {
  if (scale === '1') return;
  await page.request.post('/api/settings/ui-scale', { form: { scale } });
}

test.describe('basket never scrolls horizontally, remove button fully reachable (ut-docs#391)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('basket-scroll has no horizontal overflow at the kiosk floor width', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    for (const code of CODES) {
      await scan(page, code);
    }
    await expect(page.locator('.basket table tbody tr')).toHaveCount(CODES.length);

    const overflow = await page.evaluate(() => {
      const el = document.querySelector('.basket-scroll') as HTMLElement;
      return { scrollWidth: el.scrollWidth, clientWidth: el.clientWidth };
    });
    expect(
      overflow.scrollWidth,
      `basket-scroll must not need horizontal scroll (scrollWidth ${overflow.scrollWidth} vs clientWidth ${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);

    await page.request.post('/api/pos/reset');
    assertClean();
  });

  test('remove control is fully inside the basket panel, LTR', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await scan(page, CODES[0]);
    await expect(page.locator('.basket .btn-x')).toBeVisible();

    const boxes = await page.evaluate(() => {
      const basket = (document.querySelector('.basket-scroll') as HTMLElement).getBoundingClientRect();
      const btn = (document.querySelector('.basket .btn-x') as HTMLElement).getBoundingClientRect();
      return { basket, btn };
    });
    expect(boxes.btn.left, 'remove button left edge inside the basket').toBeGreaterThanOrEqual(boxes.basket.left - 1);
    expect(boxes.btn.right, 'remove button right edge inside the basket').toBeLessThanOrEqual(boxes.basket.right + 1);
    expect(boxes.btn.width, 'remove button must have its full width rendered, not clipped').toBeGreaterThan(20);

    await page.request.post('/api/pos/reset');
    assertClean();
  });

  test('remove control is fully inside the basket panel, RTL (fa)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await scan(page, CODES[0]);
    await expect(page.locator('.basket .btn-x')).toBeVisible();

    const boxes = await page.evaluate(() => {
      const basket = (document.querySelector('.basket-scroll') as HTMLElement).getBoundingClientRect();
      const btn = (document.querySelector('.basket .btn-x') as HTMLElement).getBoundingClientRect();
      return { basket, btn };
    });
    expect(boxes.btn.left, 'remove button left edge inside the basket (RTL)').toBeGreaterThanOrEqual(boxes.basket.left - 1);
    expect(boxes.btn.right, 'remove button right edge inside the basket (RTL)').toBeLessThanOrEqual(boxes.basket.right + 1);
    expect(boxes.btn.width, 'remove button must have its full width rendered, not clipped (RTL)').toBeGreaterThan(20);

    await page.request.post('/api/pos/reset');
    assertClean();
  });

  // A first pass at the column-width budget fixed the table-wide overflow
  // (test above) but shrank .qty-input/.disc-input just enough that a real
  // "1.00"/"0.00" value silently scrolled inside its own box, invisible to
  // every geometry assertion above (the INPUT never grew past its
  // container -- only its own text content overflowed the input itself).
  // Only the manual's regenerated docs-shots screenshot caught it. Guard
  // it directly so a future column-budget change can't reintroduce the
  // same class of bug and rely on someone eyeballing a PNG again.
  test('qty/discount inputs never scroll their own value out of view', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await scan(page, CODES[0]);
    await expect(page.locator('.qty-input').first()).toBeVisible();

    const inputs = await page.evaluate(() => {
      const read = (el: HTMLInputElement) => ({ value: el.value, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth });
      return {
        qty: read(document.querySelector('.qty-input') as HTMLInputElement),
        disc: read(document.querySelector('.disc-input') as HTMLInputElement),
      };
    });
    // 1px tolerance: legitimate sub-pixel rounding at non-integer --ui-scale
    // values (confirmed live, e.g. 101.14px computed input width) -- visibly
    // full-value, unlike the multi-px real clip this guards against.
    for (const [name, box] of Object.entries(inputs)) {
      expect(
        box.scrollWidth,
        `${name}-input value "${box.value}" must not scroll out of view (scrollWidth ${box.scrollWidth} vs clientWidth ${box.clientWidth})`,
      ).toBeLessThanOrEqual(box.clientWidth + 1);
    }

    await page.request.post('/api/pos/reset');
    assertClean();
  });
});

// Independent review of a first attempt at the fix above found it passing
// the single viewport/scale combination this file originally covered by
// under half a pixel, while failing outright -- button clipped in half
// again, negative margins up to -42px -- at ui_scale 2 (a real, offered,
// guarded-as-supported Settings option). This sweeps every scale step
// Settings offers across both documented-floor viewports so that class of
// gap can't reopen silently. Kept as its own describe (not parameterized
// into the block above) since test.use() viewport can't vary per-test
// within one describe.
for (const viewport of VIEWPORTS) {
  test.describe(`ui_scale matrix at ${viewport.label}`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    // ui_scale is a server-side setting shared by every spec on the e2e
    // suite's single, single-worker till server. A manual reset call at the
    // tail of the test body (the original pattern here) never runs when an
    // expect() earlier in the body throws, leaking a non-1 scale into every
    // later spec (ut-docs#510). Mirrors setOskMode's own
    // caller-must-restore-in-afterEach convention used elsewhere in this
    // suite (e.g. settings-osk.spec.ts) -- runs unconditionally, pass or
    // fail, and is a no-op when the test never changed the scale.
    test.afterEach(async ({ page }) => {
      await page.request.post('/api/settings/ui-scale', { form: { scale: '1' } });
    });

    for (const scale of SCALES) {
      test(`remove button + inputs stay inside the panel at ui_scale ${scale}`, async ({ page }) => {
        const assertClean = watchConsole(page);
        await setScale(page, scale);
        // Adds the line via the API directly rather than the UI scan flow:
        // at some extreme narrow-viewport+high-scale corners in this matrix
        // the scan input itself becomes unreachable (a separate, pre-existing
        // reachability gap, not what this file is about -- see .btn-x margin
        // assertions below for the thing that IS in scope). Confirmed this
        // gap predates ut-docs#391 entirely and isn't made worse by this fix.
        await page.request.post('/api/pos/scan', { form: { code: CODES[0], qty: '1' } });
        await page.goto('/');
        await expect(page.locator('.basket .btn-x')).toBeVisible();

        const data = await page.evaluate(() => {
          const scroll = document.querySelector('.basket-scroll') as HTMLElement;
          const basket = scroll.getBoundingClientRect();
          const btnEl = document.querySelector('.basket .btn-x') as HTMLElement;
          const btn = btnEl.getBoundingClientRect();
          const btnTd = btnEl.closest('td')!.getBoundingClientRect();
          const qty = document.querySelector('.qty-input') as HTMLInputElement;
          const disc = document.querySelector('.disc-input') as HTMLInputElement;
          return {
            scrollWidth: scroll.scrollWidth,
            clientWidth: scroll.clientWidth,
            btnLeftMargin: btn.left - basket.left,
            btnRightMargin: basket.right - btn.right,
            // The button must fit its OWN column, not just the panel overall --
            // a column can be individually too narrow for its content while the
            // table's total width still happens to fit (exactly how a failed
            // calc()-based attempt at this budget passed scrollWidth/clientWidth
            // while still squeezing a column below its content's real size).
            btnFitsOwnTd: btn.left >= btnTd.left - 0.5 && btn.right <= btnTd.right + 0.5,
            qtyOverflow: qty.scrollWidth - qty.clientWidth,
            discOverflow: disc.scrollWidth - disc.clientWidth,
          };
        });

        expect(data.scrollWidth, 'basket-scroll must not need horizontal scroll').toBeLessThanOrEqual(data.clientWidth);
        expect(data.btnLeftMargin, 'remove button left edge inside the basket').toBeGreaterThanOrEqual(-0.5);
        expect(data.btnRightMargin, 'remove button right edge inside the basket').toBeGreaterThanOrEqual(-0.5);
        expect(data.btnFitsOwnTd, 'remove button must fit inside its own column, not just the panel').toBe(true);
        expect(data.qtyOverflow, 'qty-input value not clipped').toBeLessThanOrEqual(1);
        expect(data.discOverflow, 'disc-input value not clipped').toBeLessThanOrEqual(1);

        await page.request.post('/api/pos/reset');
        assertClean();
      });
    }
  });
}
