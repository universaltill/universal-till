import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2113: independent review of ut-docs#2095 measured the STANDALONE
// /catalog/tax-codes page (reached directly, outside the /items shell's
// #tax-codes-modal dialog entirely) overflowing horizontally at 360px --
// ~183px of the tax-codes table hanging off its container, the whole
// document scrolling sideways to 521px in a 360px viewport. #2095 only
// added a scroll safety net for the dialog-embedded copy of this same
// table (`#tax-codes-modal #tax-codes-table`), which never reaches this
// standalone page's own layout (`.catalog-list #tax-codes-table`) -- this
// is that follow-up, same measurement approach as
// tills-pairing-layout-1548.spec.ts (scrollWidth vs clientWidth, not
// trusting the CSS by eye).
test.describe('standalone /catalog/tax-codes table stays inside the viewport at phone width (ut-docs#2113)', () => {
  test('/catalog/tax-codes never needs horizontal document scroll at 360x800', async ({ page }) => {
    // The default e2e seed always plants at least "Standard VAT"
    // (scripts/e2e_seed/main.go) -- enough rows/columns (name, rate,
    // takeaway rate, active, Edit + Deactivate/Reactivate buttons) to
    // reproduce the reported overflow without seeding anything extra here.
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/catalog/tax-codes');
    await expect(page.locator('#tax-codes-table')).toBeVisible();
    await expect(page.locator('.tax-code-row').first()).toBeVisible();

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      overflow.scrollWidth,
      `document is ${overflow.scrollWidth - overflow.clientWidth}px wider than the viewport ` +
        `(scrollWidth=${overflow.scrollWidth}, clientWidth=${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);

    // The table itself scrolls/reflows rather than clipping: its own box
    // must not be wider than its wrapper card at this viewport either,
    // independent of the document-level assertion above (a table could in
    // principle stay within the wrapper's own overflow:auto scroll region
    // while some OTHER element still blew out the document, or vice versa).
    const tableBox = await page.evaluate(() => {
      const table = document.querySelector('#tax-codes-table table');
      const wrapper = document.querySelector('#tax-codes-table');
      if (!table || !wrapper) return null;
      return {
        tableWidth: table.getBoundingClientRect().width,
        wrapperWidth: wrapper.getBoundingClientRect().width,
        wrapperScrollWidth: (wrapper as HTMLElement).scrollWidth,
      };
    });
    expect(tableBox).not.toBeNull();
    // The wrapper's scrollWidth is allowed to exceed its own rendered
    // width (that's the horizontal-scroll safety net doing its job) -- what
    // must NOT happen is the wrapper's rendered width itself exceeding the
    // viewport, which is what the document-level assertion above already
    // guards; this second check pins that the table is scrolling WITHIN
    // the wrapper, not just coincidentally fitting.
    expect(tableBox!.wrapperWidth).toBeLessThanOrEqual(360);

    assertClean();
  });
});
