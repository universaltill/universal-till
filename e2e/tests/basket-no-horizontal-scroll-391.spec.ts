import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#391: third reported recurrence of the basket scrolling
// horizontally with the remove (✕) button clipped in half at the right
// edge. Prior related cards: #213 (basket line visibility), #251 (.fee-row
// clipping on a narrow viewport), #320 (flaky e2e only seeing 3/4 lines).
//
// Root cause: `.basket-scroll` sets `overflow-y: auto` but leaves
// `overflow-x` unset. Per the CSS overflow computed-value rule ("if one of
// 'overflow-x'/'overflow-y' is 'visible' and the other isn't, the
// used value of 'visible' becomes 'auto'"), the browser silently turns
// the unset overflow-x into `auto` too -- so once the basket table's
// natural (min-content) width exceeds the panel at the kiosk's floor
// column width (22rem, app.css .pos-container), a horizontal scrollbar
// appears and the default scroll position (0) shows the table's LEFT
// edge, clipping the right-most column (the remove button) in half.
//
// The fix must shrink the table's natural min-content width to fit
// (narrower qty/discount inputs, a tighter item-column cap) rather than
// papering over it with `overflow-x: hidden`, which would trade "clipped
// half the time" for "clipped every time" -- exactly what the AC rules out.
test.use({ viewport: { width: 1024, height: 600 } }); // documented kiosk floor (7" 1024x600, ut-docs/hardware/diy-pos.md)

const CODES = ['5000000000012', '5000000000029', '5000000000036'];

async function scan(page, code: string) {
  await page.getByRole('textbox').first().fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

test.describe('basket never scrolls horizontally, remove button fully reachable (ut-docs#391)', () => {
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
    for (const [name, box] of Object.entries(inputs)) {
      expect(
        box.scrollWidth,
        `${name}-input value "${box.value}" must not scroll out of view (scrollWidth ${box.scrollWidth} vs clientWidth ${box.clientWidth})`,
      ).toBeLessThanOrEqual(box.clientWidth);
    }

    await page.request.post('/api/pos/reset');
    assertClean();
  });
});
