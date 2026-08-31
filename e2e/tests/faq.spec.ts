import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// FAQ plugin content page, driven against a REAL installed plugin (seeded by
// e2e/seed_faq from the real ut-plugin-faq content bundles — see run-till.sh)
// rather than a mocked route. Covers what only a real browser can verify:
// the client-side search JS actually filtering the DOM, and the RTL bundle
// flag actually flipping `dir` on the content card. Server-side rendering
// logic (locale fallback matching, checksum verification, keyword haystack
// construction) already has thorough coverage in
// internal/pages/plugin_page_test.go — this spec doesn't duplicate that.
test.describe('FAQ plugin page', () => {
  test('renders the installed English bundle', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/plugin/faq?lang=en');
    // Real translated label (locales/en.json, seeded alongside content/ —
    // see e2e/seed_faq), not the raw "plugin.faq.menu" key.
    await expect(page.locator('h1')).toHaveText('Help / FAQ');
    await expect(page.locator('.plugin-content-entry')).toHaveCount(6);
    await expect(page.locator('.plugin-content')).not.toHaveAttribute('dir', 'rtl');
    assertClean();
  });

  test('Persian bundle renders RTL', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/plugin/faq?lang=fa');
    await expect(page.locator('.plugin-content')).toHaveAttribute('dir', 'rtl');
    assertClean();
  });

  test('search filters entries and hides empty categories', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/plugin/faq?lang=en');
    const entries = page.locator('.plugin-content-entry');
    await expect(entries).toHaveCount(6);

    // "barcode" only matches the "How do I add an item to a sale?" entry
    // (Sales category); every entry in the General category should end up
    // hidden, hiding that whole section.
    await page.locator('#plugin-content-search').fill('barcode');
    await expect(page.locator('.plugin-content-entry:visible')).toHaveCount(1);
    await expect(page.locator('.plugin-content-entry:visible summary')).toContainText(
      'How do I add an item to a sale?',
    );
    await expect(page.locator('.plugin-content-category:visible')).toHaveCount(1);

    // Clearing the search restores everything.
    await page.locator('#plugin-content-search').fill('');
    await expect(page.locator('.plugin-content-entry:visible')).toHaveCount(6);
    assertClean();
  });

  test('an unsupported locale falls back with a notice', async ({ page }) => {
    const assertClean = watchConsole(page);
    // ja-JP has no shipped bundle in the fixture set (en-US, fa-IR only) —
    // a genuine language fallback, distinct from a same-base-language pick.
    await page.goto('/plugin/faq?lang=ja-JP');
    await expect(page.locator('.plugin-content-notice')).toBeVisible();
    assertClean();
  });
});
