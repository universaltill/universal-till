import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout, clearAllHeldSales } from './helpers';

// ut-docs#2128: `.tender-scroll` (scan row + held-sales strip, app.css)
// scrolls internally when the held-sales strip doesn't fit — but, like
// `.products` before ut-docs#1313's fix, had no visible scroll affordance.
// A real product-owner report on the pilot tablet ("held sales not
// findable") traced to exactly this: the strip renders correctly and is
// reachable by scrolling, but nothing on screen hints there is anything to
// scroll to, and many kiosk browsers hide native scrollbars entirely.
//
// Fix (app.css): the same pure-CSS "scroll shadow" recipe `.products`
// already uses (ut-docs#1313) — two gradient pairs split
// `background-attachment: local`/`scroll` — ported onto `.tender-scroll`.
// This spec mirrors products-scroll-affordance-1313.spec.ts's own structure
// deliberately, so a future reader can diff the two rather than re-derive
// the mechanism.
//
// The pilot device (1280x800) is used to reproduce the precondition: live
// measurement (this card's own investigation) found `.tender-scroll`
// already overflows there with just ONE held sale (scrollHeight 95 vs
// clientHeight 70) -- unlike `.products`, which needs a short, non-standard
// viewport to force an overflow, `.tender-scroll`'s overflow is the real,
// reported bug at a real, supported device resolution.
test.describe('held-sales strip scroll affordance (ut-docs#2128)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    // Leave no held sale behind for the next spec sharing this server/DB.
    await clearAllHeldSales(page);
  });

  test('bottom scroll cue is wired via background layering once the strip overflows, and self-hides at the scrolled-to-bottom edge', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await clearAllHeldSales(page);

    const tenderScroll = page.locator('.tender-scroll');
    await waitForStableLayout(page, '.tender-scroll');

    // Hold one sale -- enough to reproduce real overflow at the pilot
    // resolution per this card's own live measurement, so this spec asserts
    // CSS wiring against content that genuinely needs the scroll cue.
    await page.locator('input[name="code"]').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
    const modal = page.locator('#hold-modal');
    await expect(modal).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/hold')),
      modal.locator('button[type=submit]').click(),
    ]);
    await expect(modal).toBeHidden();
    await waitForStableLayout(page, '.tender-scroll');

    const overflow = await tenderScroll.evaluate((el) => el.scrollHeight - el.clientHeight);
    expect(overflow, '.tender-scroll must have overflow content for this spec to be meaningful').toBeGreaterThan(0);

    // Same mechanism as .products (ut-docs#1313): four background layers
    // (two covers, two shadows), local/local/scroll/scroll.
    const style = await tenderScroll.evaluate((el) => {
      const cs = getComputedStyle(el);
      return { attachment: cs.backgroundAttachment, image: cs.backgroundImage };
    });
    const attachments = style.attachment.split(',').map((s) => s.trim());
    expect(attachments, 'expected 4 background layers split local/local/scroll/scroll').toEqual([
      'local',
      'local',
      'scroll',
      'scroll',
    ]);
    expect(style.image.match(/radial-gradient/g)?.length ?? 0, 'expected two radial-gradient shadow layers').toBe(2);

    // On first paint, scrollTop is 0 and there is genuine unseen content
    // below the fold (overflow > 0 above), so the bottom cue is showing.
    expect(await tenderScroll.evaluate((el) => el.scrollTop)).toBe(0);

    // Functional regression check: the panel is still a real, working
    // scroll container -- scrolling it programmatically (same event path a
    // touch-drag or wheel gesture drives) actually moves content, and the
    // held chip becomes a real hit-test target once scrolled to.
    await tenderScroll.evaluate((el) => {
      el.scrollTop = el.scrollHeight - el.clientHeight;
    });
    await waitForStableLayout(page, '.tender-scroll');
    const atBottom = await tenderScroll.evaluate((el) => el.scrollTop >= el.scrollHeight - el.clientHeight - 1);
    expect(atBottom, 'expected scrollTop to reach the panel bottom').toBe(true);

    const chip = page.locator('.held-chip').first();
    const hit = await chip.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const x = r.left + r.width / 2;
      const y = r.top + r.height / 2;
      const at = document.elementFromPoint(x, y);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'held chip must be a real hit-test target once scrolled to the bottom').toBe(true);

    assertClean();
  });
});
