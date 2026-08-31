import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#298: /self-order is auth-exempt (ADR-0020 — reachable by any
// anonymous LAN client, kiosk mode or not) and had no e2e coverage at all
// before this card. Same canonical-mark, no-plate check as the login/setup
// surfaces in login.spec.ts — --surface is white in every shipped theme, so
// self-order keeps the canonical dark mark with no backing plate (only
// .nav, which is always dark, gets the light-glyph variant; an independent
// review found a light-glyph-on-a-plate here just relocated the reported
// defect rather than fixing it). This route never requires a session, so it
// gets its own light-weight spec rather than piggybacking on the AUTH
// project's serial login flow.
test.describe('self-order welcome screen brand mark', () => {
  test('the mark is the canonical dark asset with no backing plate', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/self-order');

    const logo = page.locator('.selforder-logo');
    await expect(logo).toBeVisible();
    await expect(logo).toHaveAttribute('src', /unitill-logo\.svg/);
    await expect(logo).not.toHaveAttribute('src', /unitill-logo-light\.svg/);

    const bg = await logo.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(bg, 'self-order logo must have no backing plate').toBe('rgba(0, 0, 0, 0)');

    assertClean();
  });
});
