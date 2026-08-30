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

  test('navigation uses the canonical accessible logo', async ({ page }) => {
    await page.goto('/');

    const logo = page.locator('.nav .logo img');
    // ut-docs#298: .nav is always dark (var(--brand) in every shipped
    // theme), so it uses the light-glyph variant with no backing plate —
    // the other till surfaces (login/setup/self-order) keep the canonical
    // dark mark, since their background is white in every shipped theme.
    await expect(logo).toHaveAttribute('src', /unitill-logo-light\.svg/);
    // 2026-08-30 (sale-screen compact-ui pass): the logo now sits next to
    // a visible ".app-name" text span carrying "Universal Till" (was
    // previously only in the sale screen's own <h1>, now shared across
    // every page via nav.html) -- the image is decorative and alt is
    // empty so a screen reader doesn't announce the name twice.
    await expect(logo).toHaveAttribute('alt', '');
    await expect(page.locator('.nav .app-name')).toHaveText('Universal Till');
    await expect(logo).toBeVisible();

    // ut-docs#290 shipped the previous logo renamed to unitill-logo.svg, and
    // every check of the day passed because they all matched on the filename.
    // Assert the artwork instead: it has to decode, and it has to be the
    // portrait mark (aspect ~0.73) rather than the landscape one (~1.12).
    const box = await logo.evaluate((el: HTMLImageElement) => ({
      naturalWidth: el.naturalWidth,
      naturalHeight: el.naturalHeight,
    }));
    expect(box.naturalWidth, 'logo must actually decode').toBeGreaterThan(0);
    expect(
      box.naturalWidth / box.naturalHeight,
      'canonical mark is portrait; a landscape ratio means the old logo is back',
    ).toBeLessThan(1);
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
    // Complete Sale lives in the Split tender tab, inside the payment
    // overlay (ut-docs#1252): the Pay/Split tabs used to always be on
    // screen, now they're behind the Payment button that opens
    // #payment-overlay. The tab itself still carries role="tab" (WAI-ARIA
    // tabs pattern, ut-docs#424) rather than the native <button> element's
    // implicit role="button".
    await page.getByTestId('payment-open').click();
    await page.getByRole('tab', { name: 'Split', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Complete Sale', exact: true })).toBeVisible();
  });
});
