import { test, expect } from './fixtures';

// ut-docs#1348 (product owner, live device confirmation on a real tablet):
// the sale-screen left icon rail's bottom group read visibly inconsistent
// against the top group. Two separate defects, same root cause class
// (per-glyph/per-element CSS drift from the shared `.nav-toggle` button
// pattern every other rail item uses):
//
//  1. The `?` help link kept the bare `.help-hint` markup (a small,
//     deliberately low-contrast outlined circle meant to sit next to a page
//     heading elsewhere in the app) instead of `.nav-toggle` — so it was
//     both visibly smaller than a rail button AND left-aligned rather than
//     centered (`.nav-right`'s `align-items: stretch` only centers an item
//     with no fixed size of its own; a fixed-size override falls back to
//     flex-start).
//  2. The bug-report toggle's 🐞 glyph had no `.ico-boost`, so it read
//     visibly smaller than 👤/🔒 at the same nominal font-size — the same
//     per-glyph ink-coverage gap app.css's `.ico-boost` comment already
//     documents for ☰/♻️/🔒, just not caught for this glyph in that first
//     pass.
test.describe('sale-screen nav rail icon consistency (ut-docs#1348)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('the help "?" link renders as a real .nav-toggle button, not the bare small-circle .help-hint', async ({
    page,
  }) => {
    await page.goto('/catalog');
    const help = page.locator('[data-testid="help-hint"]');
    await expect(help).toBeVisible();
    // The nav-rail instance must NOT carry the low-contrast small-circle
    // class used elsewhere in the app (settings.html etc, via helpLink) —
    // it must be a full rail button like every sibling icon.
    await expect(help).toHaveClass(/\bnav-toggle\b/);
    await expect(help).not.toHaveClass(/\bhelp-hint\b/);
    await expect(help.locator('.nav-toggle-ico')).toBeVisible();
  });

  test('the help button is horizontally centered in the rail, same as every other rail button', async ({
    page,
  }) => {
    await page.goto('/catalog');
    const help = page.locator('[data-testid="help-hint"]');
    const reference = page.locator('[data-testid="nav-menu"]'); // an ordinary .nav-toggle sibling
    await expect(help).toBeVisible();
    await expect(reference).toBeVisible();
    const helpBox = (await help.boundingBox())!;
    const refBox = (await reference.boundingBox())!;
    const helpCenter = helpBox.x + helpBox.width / 2;
    const refCenter = refBox.x + refBox.width / 2;
    // Before the fix, the fixed-size .help-hint fell back to flex-start
    // (pinned to the rail's start edge) instead of being centered like a
    // stretched .nav-toggle button, so the two centers diverged by several
    // rail-widths. A couple of px of rounding is fine; a real regression
    // here is off by a large margin, not a couple of px.
    expect(Math.abs(helpCenter - refCenter), 'help icon must be centered like every other rail icon').toBeLessThan(3);
  });

  test('the bug-report glyph carries the same .ico-boost treatment as lock (profile as the visual reference)', async ({
    page,
  }) => {
    await page.goto('/catalog');
    const bugIco = page.locator('#bugreport-toggle .nav-toggle-ico');
    await expect(bugIco).toBeVisible();
    await expect(bugIco, 'ut-docs#1348: 🐞 read visibly smaller than 👤/🔒 without the same boost').toHaveClass(
      /\bico-boost\b/,
    );
  });
});
