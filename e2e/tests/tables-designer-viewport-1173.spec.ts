import { test, expect } from '@playwright/test';
import { watchConsole, deactivateAllTables, createTable } from './helpers';

// ut-docs#1173: reported against the real 10.1" till (1920x1200 @ 1.5 scale
// — 1280x800 logical), landscape and short. The floor-plan SVG's viewBox is
// square, and the pre-fix CSS sized it to inline-size:100% with
// block-size:auto — fine on a portrait/desktop screen, but on a wide-short
// viewport that made the plan render as tall as the page is wide, pushing
// the edit toggle, the add-table form and the table list off the bottom.
// The touch-GESTURE half of the same report is #1170 (already fixed); this
// spec covers the LAYOUT half only, same split the ticket itself draws.
//
// deactivateAllTables/createTable come from helpers.ts (this ticket's own
// review found the pair duplicated verbatim across four spec files —
// consolidated there rather than adding a fifth copy).

test.describe('Table designer fits the till\'s real 1280x800 viewport (ut-docs#1173)', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('the floor plan no longer dictates page height, and every designer control stays reachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTable(page, 'E2E Viewport Table 1173');

    // The plan must never consume more than a bounded share of the actual
    // viewport height — the pre-fix bug rendered it (viewport width) tall,
    // i.e. ~1280px on an 800px-tall screen, more than the whole screen on
    // its own. min(30rem, 55vh) at 1280x800/16px root is 440px; 600 leaves
    // generous headroom for font-size/rounding/a future cap retune (e.g.
    // aligning to .catalog-list's 62vh sibling, 496px) without letting the
    // real regression class (>=780px, i.e. "as tall as the page is wide")
    // back in unnoticed.
    const svgBox = await page.locator('#floorplan').boundingBox();
    expect(svgBox, 'floor plan must render with a real, measurable box').toBeTruthy();
    expect(
      svgBox!.height,
      `floor plan block-size must be bounded, not driven by viewport width (got ${svgBox!.height}px on an 800px-tall viewport)`,
    ).toBeLessThan(600);

    // No page-level horizontal scroll at the till's real width — the plan's
    // own inline-size:100% cap must still hold now that block-size is capped
    // too (a max-block-size regression could otherwise let preserveAspectRatio
    // overflow the card horizontally instead).
    const viewportWidth = page.viewportSize()!.width;
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(
      scrollWidth,
      `page must not scroll horizontally at the till's real width (scrollWidth ${scrollWidth} vs viewport ${viewportWidth})`,
    ).toBeLessThanOrEqual(viewportWidth);

    // Reachability, proved the same way tender-panel-reachable.spec.ts holds
    // itself to: a real, completing interaction, not just a geometry check.
    // Enter edit mode (the toggle itself may already be off-screen pre-fix).
    const toggle = page.locator('#tables-edit-toggle');
    await toggle.scrollIntoViewIfNeeded();
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect(page.locator('#floorplan-section')).toHaveClass(/editing/);

    // The bottom-of-page add-table form's Save button — the control the
    // original report specifically said it could no longer reach.
    const addSubmit = page.locator('.users-form form[action="/api/tables"] button[type=submit]');
    await addSubmit.scrollIntoViewIfNeeded();
    await expect(addSubmit).toBeVisible();
    await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill('E2E Reachable Table 1173');
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), addSubmit.click()]);
    await expect(page.locator('.table', { hasText: 'E2E Reachable Table 1173' })).toBeVisible();

    // The row list itself must not need horizontal scrolling to reach its
    // own Save button (ut-docs#1173 review finding): capping .users-list's
    // table width (app.css) made this page's own wide inline-edit row --
    // rename + zone + seats inputs, a shape select, and two buttons across
    // two forms, the widest row of any .users-list consumer -- push its
    // Save button behind an in-card horizontal scroll with no visible
    // affordance a touchscreen operator could discover. `.click()` alone
    // does NOT catch this: Playwright auto-scrolls an element's nearest
    // scrollable ancestor into view before clicking, so a click succeeds
    // whether or not a real operator could ever find that scroll -- this
    // reproduced with the fix below deliberately disabled while writing the
    // test. The scrollWidth-vs-clientWidth check (same pattern
    // basket-no-horizontal-scroll-391.spec.ts uses for the identical bug
    // class) is what actually distinguishes "fits" from "technically
    // present, invisibly scrolled".
    const table = page.locator('.users-list .table');
    const tableBox = await table.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }));
    expect(
      tableBox.scrollWidth,
      `the table row must fit its card without needing horizontal scroll to reach Save (scrollWidth ${tableBox.scrollWidth} vs clientWidth ${tableBox.clientWidth})`,
    ).toBeLessThanOrEqual(tableBox.clientWidth + 1); // 1px tolerance: sub-pixel rounding, not a real clip

    // And the row's own Save button really is a real, completing click at
    // its natural (non-scrolled) position -- same reachability standard as
    // tender-panel-reachable.spec.ts.
    const rowSave = page
      .locator('.users-list tbody tr')
      .first()
      .locator('form[action^="/api/tables/"]:not([action$="/active"]) button[type=submit]');
    await expect(rowSave).toBeVisible();
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), rowSave.click()]);

    assertClean();
  });
});
