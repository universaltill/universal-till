import { test, expect } from '@playwright/test';
import { ensureOperator, watchConsole } from './helpers';

// ut-docs#1423, auth-project half: the session chip (users/promotions/
// translations/profile/LOCK) only renders with a real session (see
// playwright.config.ts's AUTH_ONLY_SPECS note), and 🔒 was one of the two
// icons the product owner reported small on the tablet three times over.
// With every rail icon now one inline SVG set, the full 11-icon rail must
// render at one identical box size — lock included, measured here.
test.describe('nav rail SVG icons: full manager rail incl. lock (ut-docs#1423)', () => {
  test('all 11 rail icons are SVG and share one rendered box size', async ({ page }) => {
    const assertClean = watchConsole(page);
    await ensureOperator(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings');
    await expect(page.locator('.session-admin-link')).toHaveCount(3);
    await expect(page.locator('.session-lock svg[data-icon="lock"]')).toBeVisible();
    await expect(page.locator('#bugreport-toggle svg[data-icon="bug"]')).toBeVisible();

    const svgs = page.locator('.nav .nav-toggle-ico svg[data-icon]');
    const n = await svgs.count();
    expect(n, 'till/menu/inventory/orders/help/bug/users/tag/globe/user/lock').toBe(11);

    const boxes: { icon: string; w: number; h: number }[] = [];
    for (let i = 0; i < n; i++) {
      const el = svgs.nth(i);
      const box = (await el.boundingBox())!;
      boxes.push({ icon: (await el.getAttribute('data-icon')) ?? `#${i}`, w: box.width, h: box.height });
    }
    const ref = boxes[0];
    for (const b of boxes) {
      expect(Math.abs(b.w - ref.w), `${b.icon} width differs from ${ref.icon}`).toBeLessThan(1);
      expect(Math.abs(b.h - ref.h), `${b.icon} height differs from ${ref.icon}`).toBeLessThan(1);
      expect(b.w, `${b.icon} must render at a real size`).toBeGreaterThan(16);
    }
    expect(boxes.map((b) => b.icon)).toEqual(expect.arrayContaining(['lock', 'user', 'bug']));

    // No emoji/text left anywhere in the rail's icon boxes.
    const leftover = await page.locator('.nav .nav-toggle-ico').evaluateAll((els) =>
      els.map((e) => (e.textContent || '').trim()).filter((t) => t !== ''),
    );
    expect(leftover, 'rail icon boxes must carry no text/emoji').toEqual([]);
    assertClean();
  });

  test('every rail tile (a and button alike) has one width and one background', async ({ page }) => {
    // Seen on the tablet alongside the icon-size report: the two rail items
    // that are <button>s (bug-report, lock) rendered narrower than the <a>
    // tiles around them — a <button> shrink-wraps its content unless it is
    // given an explicit inline-size, even inside a stretch flex column.
    await ensureOperator(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings');
    await expect(page.locator('.session-lock button.btn-lock')).toBeVisible();
    await expect(page.locator('#bugreport-toggle')).toBeVisible();
    const tiles = await page.locator('.nav .nav-toggle').evaluateAll((els) =>
      els
        .filter((e) => (e as HTMLElement).offsetParent !== null)
        .map((e) => {
          const r = e.getBoundingClientRect();
          const cs = getComputedStyle(e);
          return { tag: e.tagName, id: e.id || e.className, w: r.width, bg: cs.backgroundColor };
        }),
    );
    expect(tiles.length).toBeGreaterThanOrEqual(11);
    const ref = tiles[0];
    for (const t of tiles) {
      expect(Math.abs(t.w - ref.w), `${t.tag} ${t.id} width ${t.w} vs ${ref.w}`).toBeLessThan(1);
      expect(t.bg, `${t.tag} ${t.id} background`).toBe(ref.bg);
    }
  });
});
