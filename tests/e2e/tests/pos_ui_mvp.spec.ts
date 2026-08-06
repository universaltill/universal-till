import { test, expect } from '../support/fixtures';

test.describe('POS UI MVP Uplift', () => {
  test('kiosk shell renders key cashier flow entrypoints', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expect(page.getByTestId('kiosk-checkout-start')).toBeVisible();
    await expect(page.getByTestId('kiosk-inventory-link')).toBeVisible();
  });

  test('plugin entrypoints are accessible from navigation', async ({ page }) => {
    await page.goto('/');

    // Touch nav: the sale screen shows a ☰ Menu button that opens the menu
    // page of big touch tiles; Help/Support is a tile there.
    await page.getByTestId('nav-menu').click();
    await page.locator('.menu-tile[href="/help"]').click();
    await expect(page.getByTestId('plugin-faq-entry')).toBeVisible();
  });

  test('navigation uses the canonical accessible theme-aware logo', async ({ page }) => {
    await page.goto('/');

    // ut-docs#298: the brand mark moved from <img src="unitill-logo.svg"> to
    // a CSS-masked <span class="brand-mark">, so it can be filled with
    // currentColor and follow the surface's own text color instead of
    // needing a hardcoded light plate behind it. Assert the mechanics that
    // replace the old <img> checks:
    const logo = page.locator('.nav .logo .brand-mark');
    await expect(logo).toHaveAttribute('role', 'img');
    await expect(logo).toHaveAttribute('aria-label', 'Universal Till');
    await expect(logo).toBeVisible();

    const style = await logo.evaluate((el) => {
      const cs = getComputedStyle(el);
      const box = el.getBoundingClientRect();
      return {
        mask: cs.maskImage || (cs as any).webkitMaskImage,
        bg: cs.backgroundColor,
        width: box.width,
        height: box.height,
      };
    });
    // It has to actually reference the canonical artwork...
    expect(style.mask, 'brand-mark must mask the canonical svg').toContain('unitill-logo.svg');
    // ...render with a real, visible fill (currentColor resolved to nav's
    // white text), not a transparent/invisible one...
    expect(style.bg, 'brand-mark must resolve currentColor to an opaque fill').toBe('rgb(255, 255, 255)');
    // ...and it has to be the portrait mark (aspect ~0.7336) rather than the
    // landscape one (~1.12) — ut-docs#290 shipped the previous logo renamed
    // to unitill-logo.svg, and every filename-only check passed that day.
    expect(style.height, 'brand-mark must actually take up space').toBeGreaterThan(0);
    expect(
      style.width / style.height,
      'canonical mark is portrait; a landscape ratio means the old logo is back',
    ).toBeLessThan(1);

    // The regression itself (ut-docs#298): a white tile pasted behind the
    // mark on the till's dark header. Assert the wrapper carries no light
    // plate anymore — the mark now belongs to the header, not a patch on it.
    const wrapperBg = await page.locator('.nav .logo').evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(wrapperBg, 'nav .logo must not carry a hardcoded light plate').toBe('rgba(0, 0, 0, 0)');
  });

  test('login screen renders the theme-aware logo on its light surface', async ({ page }) => {
    // /setup and /login share the exact same .login-logo markup/CSS
    // (ut-docs#298 covers both); /setup is used here because it's always
    // reachable regardless of first-boot/PIN state, unlike /login, which
    // redirects to /setup on a fresh till with no operator PIN yet.
    await page.goto('/setup');

    const logo = page.locator('.setup-card .brand-mark.login-logo');
    await expect(logo).toBeVisible();
    const bg = await logo.evaluate((el) => getComputedStyle(el).backgroundColor);
    // Default theme's --text is #0f172a — the mark must pick that up via
    // currentColor rather than staying on a fixed color or vanishing.
    expect(bg, 'login logo must resolve currentColor to the surface text color').toBe('rgb(15, 23, 42)');
  });

  test('offline status indicator is present and non-blocking', async ({ page }) => {
    await page.goto('/');

    const status = page.getByTestId('status-indicator');
    await expect(status).toBeVisible();
    await expect(status).toHaveText(/offline|online/i);
  });

  test('accessibility baseline for primary actions', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByRole('button', { name: 'Add', exact: true })).toBeVisible();
    // Complete Sale lives in the Split tender tab since the tabbed tender panel.
    await page.getByRole('button', { name: 'Split', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Complete Sale', exact: true })).toBeVisible();
  });
});
