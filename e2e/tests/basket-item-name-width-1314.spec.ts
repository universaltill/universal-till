import { test, expect } from '@playwright/test';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#1314: basket line-item names truncated unreadably ("Cheddar
// Cheese 400g" -> "Chedd Che..."). Root cause: the QTY column reserved
// 8rem (two side-by-side 3.4rem inputs) out of a 22-26.25rem panel, so
// after PRICE/TOTAL/remove the ITEM column — the one thing the operator
// must be able to read to trust the sale — was left with as little as
// ~4.35rem of text space at the panel's default width.
//
// Fix (4th attempt — app.css's ".basket table" comment carries the
// history of the three failed ones): stack qty above discount inside
// .line-inputs (flex column), shrinking the QTY column's reserved rem
// budget so the undeclared ITEM column (table-layout: fixed gives the
// single width-less column all leftover space) gets the freed width.
//
// Measured, not eyeballed: a clamped/clipped .line-name is detectable as
// scrollHeight > clientHeight (the -webkit-line-clamp:2 + overflow:hidden
// pair hides the third-and-later lines) and scrollWidth > clientWidth (a
// long unbreakable word clipped mid-character). Both reads are the same
// getBoundingClientRect/scroll* style every sibling spec here uses.
const CHEDDAR = '5000000000104'; // itm010 "Cheddar Cheese 400g" (001_init.sql + 023 checksum fix)

async function scan(page, code: string) {
  await page.getByRole('textbox').first().fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

// A name is "fully readable" when nothing of it was clipped away: no
// vertical clamp overflow (every line rendered) and no horizontal clip.
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

for (const viewport of [
  { width: 1024, height: 600, label: 'kiosk floor 1024x600' },
  { width: 1280, height: 800, label: 'default till 1280x800' },
]) {
  test.describe(`basket item names stay readable at ${viewport.label} (ut-docs#1314)`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test.afterEach(async ({ page }) => {
      await page.request.post('/api/pos/reset');
    });

    test('a real 19-char product name renders in full, not ellipsis-clamped', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await scan(page, CHEDDAR);
      await expect(page.locator('.basket .line-name')).toHaveText('Cheddar Cheese 400g');

      const name = await nameClipState(page);
      expect(
        name.scrollHeight,
        `"${name.text}" must not be vertically clamped away (scrollHeight ${name.scrollHeight} vs clientHeight ${name.clientHeight}, rendered width ${name.renderedWidth}px)`,
      ).toBeLessThanOrEqual(name.clientHeight + 1);
      expect(
        name.scrollWidth,
        `"${name.text}" must not be horizontally clipped (scrollWidth ${name.scrollWidth} vs clientWidth ${name.clientWidth})`,
      ).toBeLessThanOrEqual(name.clientWidth + 1);
      assertClean();
    });

    test('a long German compound name wraps readably instead of clipping mid-word', async ({ page }) => {
      // German product names run long as single unbreakable words — the
      // exact case a 2-line clamp with no overflow-wrap clips mid-character.
      // Rewriting the rendered text node (rather than seeding a throwaway
      // item on the shared e2e server) exercises the identical CSS layout
      // path; the server-rendered case is covered by the test above.
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
        `"${name.text}" must fit its line clamp un-truncated (scrollHeight ${name.scrollHeight} vs clientHeight ${name.clientHeight}, rendered width ${name.renderedWidth}px)`,
      ).toBeLessThanOrEqual(name.clientHeight + 1);
      assertClean();
    });
  });
}
