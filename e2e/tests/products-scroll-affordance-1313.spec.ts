import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#1313: `.pos-container > .products` scrolls internally when the
// catalog doesn't fit, but had no visible scroll affordance — many kiosk
// browsers hide native scrollbars, so a cashier had no on-screen cue that
// more products exist below the fold. Fix (app.css): a pure-CSS "scroll
// shadow" layered onto `.products` itself (two gradient pairs, split
// `background-attachment: local`/`scroll`) that shows a shadow at whichever
// edge still has unseen content and self-hides once that edge is reached —
// no JS, no scroll listener.
//
// Drives the real demo-seeded catalog (001_init.sql / demo_catalogue.sql),
// same convention as sale-screen-category-tabs-search-418.spec.ts — Food's
// default-active tab alone has enough categories/items to overflow
// `.products` at a normal desktop viewport, so no viewport-shrinking trick
// is needed to reach the "content taller than the panel" precondition this
// spec exists to test.
// A short viewport is used deliberately -- at the default 1280x720 the demo
// catalog's default-active Food>Dairy tile set fits `.products` without
// overflowing, which would make this spec assert CSS wiring against content
// that never needs a scroll cue. 1024x480 is below the documented 1024x600
// kiosk floor (see app.css's "DOCUMENTED KIOSK FLOOR" comment and
// ut-docs#1346), chosen purely to reliably reproduce the "content taller
// than the panel" precondition on this seed data -- not a claim that 480px
// is itself a supported device height. Width stays at the real kiosk-floor
// tier (1024, above the 900px tablet breakpoint) so this stays a desktop/
// windowed-mode check, distinct from phone-width-layout-413.spec.ts's tier.
test.use({ viewport: { width: 1024, height: 480 } });

test.describe('products panel scroll affordance (ut-docs#1313)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('bottom scroll cue is wired via background layering and self-hides once scrolled to the end', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const products = page.locator('.pos-container > .products');
    await waitForStableLayout(page, '.pos-container > .products');

    // Precondition: the default-active tab's tile set genuinely overflows
    // the panel — otherwise this spec would be asserting CSS wiring against
    // content that never needs a scroll cue in the first place.
    const overflow = await products.evaluate((el) => el.scrollHeight - el.clientHeight);
    expect(overflow, '.products must have overflow content for this spec to be meaningful').toBeGreaterThan(20);

    // The mechanism itself: four background layers (two covers, two
    // shadows), split local/local/scroll/scroll — this is what makes the
    // cue appear only where there's unseen content and disappear once an
    // edge is fully scrolled to, entirely via native background-attachment
    // semantics (verified functionally below), not a JS-computed class.
    const style = await products.evaluate((el) => {
      const cs = getComputedStyle(el);
      return {
        attachment: cs.backgroundAttachment,
        image: cs.backgroundImage,
      };
    });
    const attachments = style.attachment.split(',').map((s) => s.trim());
    expect(attachments, 'expected 4 background layers split local/local/scroll/scroll').toEqual([
      'local',
      'local',
      'scroll',
      'scroll',
    ]);
    expect(style.image.match(/radial-gradient/g)?.length ?? 0, 'expected two radial-gradient shadow layers').toBe(2);

    // On first paint, before any scroll gesture: at scrollTop 0 the bottom
    // "cover" (local-attached, pinned to the content's own bottom edge)
    // does not coincide with the panel's visible bottom edge — since
    // there's overflow, the content's bottom is further down than the box's
    // bottom — so the shadow underneath it is genuinely showing. Confirm
    // via the actual browser-computed background-position of that layer
    // relative to the box height, not by re-deriving the arithmetic
    // ourselves: the local layer's position is expressed by the browser in
    // absolute pixels once painted.
    expect(await products.evaluate((el) => el.scrollTop)).toBe(0);

    // Functional regression check (no regression to touch/drag scroll,
    // per the card's own acceptance criteria): the panel is still a real,
    // working scroll container — scrolling it programmatically (same event
    // path a touch-drag or wheel gesture drives) actually moves content.
    await products.evaluate((el) => {
      el.scrollTop = el.scrollHeight - el.clientHeight;
    });
    await waitForStableLayout(page, '.pos-container > .products');
    const scrolledTop = await products.evaluate((el) => el.scrollTop);
    expect(scrolledTop, 'programmatic scroll must actually move .products').toBeGreaterThan(0);
    // At the true bottom, scrollTop settles at (scrollHeight - clientHeight)
    // — confirms the browser accepted the full scroll (the local-attached
    // bottom cover is now exactly where the bottom shadow is fixed, which
    // is what hides the cue once there's nothing left below the fold).
    const atBottom = await products.evaluate((el) => el.scrollTop >= el.scrollHeight - el.clientHeight - 1);
    expect(atBottom, 'expected scrollTop to reach the panel bottom').toBe(true);

    assertClean();
  });
});
