import { test, expect } from './fixtures';

// ut-docs#1423 (product owner, live on the tablet, third report of the same
// symptom): the rail's 🔒 lock and 🐞 bug-report icons still rendered
// visibly smaller than their siblings after #1332's and #1348's per-glyph
// emoji font-size bumps, because each platform's colour-emoji font pads each
// glyph differently — a bump tuned on desktop Chromium does not carry to
// Android. The fix is structural: every rail icon is one inline SVG set
// ({{ icon }} → internal/httpx/icons.go) sized only by CSS, so all icons
// share one rendered box on every platform. These tests pin the structure
// (no text/emoji left, one SVG per icon) and the parity (identical boxes).
test.describe('sale-screen nav rail icons are one SVG set (ut-docs#1423)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('every rail icon is an inline SVG with no text content', async ({ page }) => {
    await page.goto('/catalog');
    // Chips (bugreport/session) are htmx fragments that load after render.
    await expect(page.locator('#bugreport-toggle')).toBeVisible();
    // The session chip (profile/lock) needs a real session — covered by
    // nav-rail-svg-icons-lock-1423.spec.ts on the auth project.
    const icons = page.locator('.nav .nav-toggle-ico');
    const n = await icons.count();
    expect(n, 'rail should carry till/menu/inventory/orders/help/bug without a session').toBeGreaterThanOrEqual(6);
    for (let i = 0; i < n; i++) {
      const ico = icons.nth(i);
      await expect(ico.locator('svg[data-icon]'), `rail icon #${i} must be an inline SVG`).toHaveCount(1);
      const text = (await ico.innerText()).trim();
      expect(text, `rail icon #${i} must carry no text/emoji (${JSON.stringify(text)})`).toBe('');
    }
  });

  test('all rail icons render at one identical box size (bug included; lock on the auth project)', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('#bugreport-toggle svg[data-icon="bug"]')).toBeVisible();
    const svgs = page.locator('.nav .nav-toggle-ico svg');
    const n = await svgs.count();
    const boxes: { icon: string; w: number; h: number }[] = [];
    for (let i = 0; i < n; i++) {
      const el = svgs.nth(i);
      const box = (await el.boundingBox())!;
      boxes.push({ icon: (await el.getAttribute('data-icon')) ?? `#${i}`, w: box.width, h: box.height });
    }
    const ref = boxes[0];
    for (const b of boxes) {
      // Sub-pixel rounding only; the emoji regression was whole pixels.
      expect(Math.abs(b.w - ref.w), `${b.icon} width differs from ${ref.icon}`).toBeLessThan(1);
      expect(Math.abs(b.h - ref.h), `${b.icon} height differs from ${ref.icon}`).toBeLessThan(1);
      expect(b.w, `${b.icon} must render at a real size`).toBeGreaterThan(16);
    }
    const names = boxes.map((b) => b.icon);
    expect(names, 'bug — one of the two icons that regressed twice — must be in the parity set').toEqual(
      expect.arrayContaining(['bug', 'help', 'receipt']),
    );
  });

  test('phone-width top bar still shows icon + visible label per button', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/catalog');
    const till = page.locator('[data-testid="nav-till"]');
    await expect(till.locator('svg[data-icon="receipt"]')).toBeVisible();
    await expect(till.locator('.nav-toggle-label')).toBeVisible();
  });
});
