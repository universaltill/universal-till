import { test, expect } from '@playwright/test';
import { watchConsole, ensureOperator } from './helpers';

// ut-docs#1173: reported against the real 10.1" till (1920x1200 @ 1.5 scale
// -- 1280x800 logical), landscape and short. The floor-plan SVG's viewBox is
// square, and the pre-fix CSS sized it to inline-size:100% with
// block-size:auto -- fine on a portrait/desktop screen, but on a wide-short
// viewport that made the plan render as tall as the page is wide, pushing
// the edit toggle, the add-table form and the table list off the bottom.
// The touch-GESTURE half of the same report is #1170 (already fixed); this
// spec covers the LAYOUT half only, same split the ticket itself draws.
//
// ensureOperator() is a no-op under UT_AUTH=off (the default project this
// spec runs on -- see tables-keyboard-reposition-826.spec.ts's own note on
// why /tables doesn't need the AUTH project since ut-docs#902).

async function deactivateAllTables(page) {
  await ensureOperator(page);
  await page.goto('/tables');
  for (;;) {
    const btn = page.locator('form[action$="/active"] button', { hasText: 'Deactivate' }).first();
    if ((await btn.count()) === 0) break;
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), btn.click()]);
  }
}

async function createTable(page, label: string) {
  await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill(label);
  await Promise.all([
    page.waitForURL((u) => u.pathname === '/tables'),
    page.locator('.users-form form[action="/api/tables"] button[type=submit]').click(),
  ]);
}

test.describe('Table designer fits the till\'s real 1280x800 viewport (ut-docs#1173)', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('the floor plan no longer dictates page height, and every designer control stays reachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await deactivateAllTables(page);
    await createTable(page, 'E2E Viewport Table 1173');

    // The plan must never consume more than a bounded share of the actual
    // viewport height -- the pre-fix bug rendered it (viewport width) tall,
    // i.e. ~1280px on an 800px-tall screen, more than the whole screen on
    // its own. min(30rem, 55vh) at 1280x800/16px root is 440px (55vh); give
    // generous headroom above that for font-size/rounding without letting
    // the regression (>=780px, i.e. "as tall as the page is wide") back in.
    const svgBox = await page.locator('#floorplan svg, #floorplan').first().boundingBox();
    // #floorplan IS the <svg id="floorplan">, so the locator above resolves
    // to one element either way -- kept broad in case a future wrapper
    // changes the id's owner.
    expect(svgBox, 'floor plan must render with a real, measurable box').toBeTruthy();
    expect(
      svgBox!.height,
      `floor plan block-size must be bounded, not driven by viewport width (got ${svgBox!.height}px on an 800px-tall viewport)`,
    ).toBeLessThan(500);

    // No page-level horizontal scroll at the till's real width -- the plan's
    // own inline-size:100% cap must still hold now that block-size is capped
    // too (a max-block-size regression could otherwise let preserveAspectRatio
    // overflow the card horizontally instead).
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth, `page must not scroll horizontally at 1280px (scrollWidth ${scrollWidth})`).toBeLessThanOrEqual(1280);

    // Reachability, proved the same way tender-panel-reachable.spec.ts holds
    // itself to: a real, completing interaction, not just a geometry check.
    // Enter edit mode (the toggle itself may already be off-screen pre-fix).
    const toggle = page.locator('#tables-edit-toggle');
    await toggle.scrollIntoViewIfNeeded();
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect(page.locator('#floorplan-section')).toHaveClass(/editing/);

    // The bottom-of-page add-table form's Save button -- the control the
    // original report specifically said it could no longer reach.
    const addSubmit = page.locator('.users-form form[action="/api/tables"] button[type=submit]');
    await addSubmit.scrollIntoViewIfNeeded();
    await expect(addSubmit).toBeVisible();
    await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill('E2E Reachable Table 1173');
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), addSubmit.click()]);
    await expect(page.locator('.table', { hasText: 'E2E Reachable Table 1173' })).toBeVisible();

    assertClean();
  });
});
