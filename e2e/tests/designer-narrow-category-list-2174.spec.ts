import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2174 (Tester finding, not Dev's original scope): the Designer's
// category-management row (buttons.html's "designer-categories" section,
// .designer-cat) is a 3-column CSS grid — "swatch text actions" — with the
// actions column (4 icon buttons: move up/down, edit, deactivate) sized
// `auto`. At a real kiosk/phone-portrait width (360px, this suite's other
// narrow-viewport checks' own reference size) that `auto` column claims its
// full intrinsic width before the `minmax(0, 1fr)` text column gets
// anything, squeezing the category name down to a ~26px sliver and
// wrapping it mid-word ("Cleaning" -> "Cle" / "aning" on separate lines) —
// confirmed visually with a real screenshot before this spec was written.
// This is a layout regression a screenshot review catches and a plain
// "does the row exist" test would not: the row is present, populated and
// non-overlapping by every DOM-shape check, just unreadable.
test.use({ viewport: { width: 360, height: 720 } });

test.describe('Designer category list stays readable at 360px (ut-docs#2174)', () => {
  test('category name column keeps real width instead of collapsing to a sliver', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/designer');
    const row = page.locator('.designer-cat').first();
    await expect(row).toBeVisible();

    const textBox = await row.locator('.designer-cat-text').boundingBox();
    expect(textBox, 'category text column must have a bounding box').not.toBeNull();
    // Before the fix this measured ~26px on a real 360px viewport (the
    // actions column's own intrinsic width left almost nothing for text);
    // 100px is comfortably above that failure and comfortably below what a
    // fixed, non-squeezed layout gives (~250-300px), so this catches a
    // real regression without being a pixel-exact snapshot.
    expect(textBox!.width, 'category name column width').toBeGreaterThan(100);

    // The action buttons (real 3rem/48px touch targets, ut-docs#2218) must
    // still be fully inside the viewport, not just present in the DOM.
    const actionButtons = row.locator('.designer-cat-actions button');
    const count = await actionButtons.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      const box = await actionButtons.nth(i).boundingBox();
      expect(box, `action button ${i} bounding box`).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(360);
    }
    assertClean();
  });
});
