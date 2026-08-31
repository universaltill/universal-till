import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#1354: regression from #1221's move-up/move-down/remove buttons.
// #buttons-grid-admin shares .grid with the sale-screen product grid, whose
// column floor was independently narrowed to 7rem (app.css:1034, product-
// owner density change) for that unrelated context. At 7rem a tile has far
// less content width than the ~154px (46px x 3 buttons + two .5rem gaps)
// #1221's touch-target-sized button row needs, so .btn-actions — a plain
// flex row with no wrap — overflows the tile's right edge and bleeds into
// the next tile. Fixed by scoping a wider column floor (11rem) to
// #buttons-grid-admin specifically, leaving the shared .grid class (and the
// sale-screen product grid that also uses it) untouched.
//
// This reproduces at the admin grid's OWN minimum column width, not a wide
// desktop viewport — #1221 and #1219 both already flagged this exact class
// of bug (passes wide, breaks at the till's real narrow layout) for this
// codebase, and the original overflow was invisible above ~600px wide.

test.describe('Designer reorder buttons stay inside their tile (ut-docs#1354)', () => {
  test('move-up/move-down/remove row never overflows its tile at a narrow viewport', async ({ page }) => {
    const assertClean = watchConsole(page);
    // The till's actual kiosk viewport (matches other layout regression
    // specs in this suite) — NOT arbitrary: CSS Grid's auto-fill only
    // drives columns down to their minmax() floor once enough tiles exist
    // to fill a wide-enough row; at a narrow phone viewport there are too
    // few columns for the floor to bind at all (1fr just stretches the one
    // or two tiles that fit to whatever space is free, which is nowhere
    // near the floor and never overflows). 1024x600 with the demo catalog's
    // 10 shortcuts is what actually drives multiple columns down to the
    // #buttons-grid-admin floor — confirmed by screenshot to reproduce
    // #1354's reported bleed (a red X button overlapping the next tile)
    // before this fix, at exactly this viewport.
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/designer');

    const grid = page.locator('#buttons-grid-admin');
    const tiles = grid.locator('.reorderable-tile');
    const tileCount = await tiles.count();
    expect(tileCount, 'designer needs at least two demo shortcut tiles to test tile layout').toBeGreaterThan(1);
    await waitForStableLayout(page, '#buttons-grid-admin, .reorderable-tile, .btn-actions');

    // .btn-actions itself is a block-level flex container with no explicit
    // width, so its OWN box just fills the tile's content width regardless
    // of whether its children overflow it — `flex-wrap` is not set, so an
    // overflowing child escapes the container's box without enlarging it.
    // The actual overflow is on the buttons/form (the flex ITEMS), so
    // measure the rightmost direct child of .btn-actions, not the
    // .btn-actions box.
    const boxes = await tiles.evaluateAll((els) =>
      els.map((el) => {
        const tileRect = (el as HTMLElement).getBoundingClientRect();
        const kids = Array.from(el.querySelector('.btn-actions')?.children ?? []) as HTMLElement[];
        const rightmostKidRight = kids.length ? Math.max(...kids.map((k) => k.getBoundingClientRect().right)) : null;
        return {
          tile: { left: tileRect.left, right: tileRect.right },
          rightmostKidRight,
        };
      }),
    );

    for (let i = 0; i < boxes.length; i++) {
      const { tile, rightmostKidRight } = boxes[i];
      expect(rightmostKidRight, `tile ${i} must render move-up/move-down/remove buttons`).not.toBeNull();
      // The button row must stay within its OWN tile's right edge (a small
      // epsilon absorbs subpixel rounding, not the ~8-30px overflow #1354
      // reported).
      expect(rightmostKidRight!, `tile ${i}'s reorder buttons overflowed its own tile's right edge`).toBeLessThanOrEqual(
        tile.right + 1,
      );

      // The reported symptom: an overflowing button row visibly bleeding
      // into the NEXT tile. Only meaningful for tiles that actually have a
      // following sibling on the same row (a tile alone at the end of a row
      // has open space to its right that isn't a neighboring tile).
      const next = boxes[i + 1];
      if (next && next.tile.left > tile.left) {
        expect(
          rightmostKidRight!,
          `tile ${i}'s reorder buttons bled into the start of tile ${i + 1}`,
        ).toBeLessThanOrEqual(next.tile.left + 1);
      }
    }

    assertClean();
  });
});
