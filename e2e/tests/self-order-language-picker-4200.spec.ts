import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4200: the self-order start screen lists every installed language
// by its own name; one tap starts the order in it (RTL for ar/fa), and the
// walk-up start screen puts the next customer back in the shop's language.
// Auth-exempt route, so it needs no session (same shape as
// self-order-brand-mark-298.spec.ts).
for (const vp of [
  { name: 'kiosk 1024x600', width: 1024, height: 600 },
  { name: 'phone 360x740', width: 360, height: 740 },
]) {
  test(`self-order language picker (${vp.name})`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const assertClean = watchConsole(page);
    await page.goto('/self-order');

    const picker = page.locator('nav.selforder-langs');
    await expect(picker).toBeVisible();
    const buttons = picker.locator('a.selforder-lang');
    const n = await buttons.count();
    expect(n, 'the e2e till ships en + ar + fa + tr at least').toBeGreaterThanOrEqual(4);

    // Every button is a real touch target, fully inside the viewport width
    // (no horizontal scroll at phone width), and the buttons never overlap.
    const boxes = [];
    for (let i = 0; i < n; i++) {
      const b = await buttons.nth(i).boundingBox();
      expect(b).not.toBeNull();
      expect(b!.height).toBeGreaterThanOrEqual(48);
      expect(b!.x).toBeGreaterThanOrEqual(0);
      expect(b!.x + b!.width).toBeLessThanOrEqual(vp.width);
      boxes.push(b!);
    }
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i], b = boxes[j];
        const overlap = a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
        expect(overlap, `language buttons ${i} and ${j} overlap`).toBe(false);
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(vp.width);
    await expect(picker.locator('a[aria-current="true"]')).toHaveCount(1);
    await page.screenshot({ path: testInfo.outputPath('start.png'), fullPage: true });

    await picker.locator('a[lang="fa"]').click();
    await expect(page).toHaveURL(/\/self-order\/shop$/);
    await expect(page.locator('html')).toHaveAttribute('lang', 'fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await page.screenshot({ path: testInfo.outputPath('shop-fa.png'), fullPage: true });

    // Back / idle reset / end of order all land on the walk-up start screen.
    await page.goto('/self-order');
    await expect(page.locator('html')).toHaveAttribute('dir', 'ltr');
    await expect(page.locator('html')).not.toHaveAttribute('lang', 'fa');

    assertClean();
  });
}
