import { test, expect } from '@playwright/test';

// The user manual (/help): the layer Go tests can't see — the htmx swap when
// you pick a topic, the debounced search, the RTL mirror, and the contextual
// "?" actually resolving from a real page.

test('manual lists topics and opens one without a full page load', async ({ page }) => {
  await page.goto('/help');
  await expect(page.locator('.manual-nav')).toBeVisible();
  await expect(page.locator('.manual-link').first()).toBeVisible();

  // htmx swaps only the panel, and pushes the URL so the topic is linkable.
  await page.getByRole('link', { name: 'Catalog, variants & barcodes' }).click();
  await expect(page.locator('#manual-topic')).toHaveAttribute('data-topic', 'catalog');
  await expect(page).toHaveURL(/\/help\/catalog$/);
  await expect(page.locator('.manual-nav')).toBeVisible(); // shell survived the swap
});

test('search narrows to matching topics', async ({ page }) => {
  await page.goto('/help');
  await page.fill('#manual-q', 'barcode');
  await expect(page.locator('.manual-result-list li')).not.toHaveCount(0);
  await expect(page.locator('.manual-result-list')).toContainText('Catalog');

  await page.fill('#manual-q', 'zzzznotathing');
  await expect(page.locator('.manual-noresults')).toBeVisible();
});

test('a topic URL loads standalone', async ({ page }) => {
  await page.goto('/help/quickstart');
  await expect(page.locator('#manual-topic')).toHaveAttribute('data-topic', 'quickstart');
  await expect(page.locator('.manual-nav')).toBeVisible();
});

// The manual must not claim English is an untranslated fallback — the till's
// default locale is a regional tag (en-US) while the topic directories are
// bare language codes, and matching those naively stamped "not translated
// yet" across the whole English manual.
test('English topics are not flagged as untranslated', async ({ page }) => {
  await page.goto('/help/sell');
  await expect(page.locator('.manual-untranslated')).toHaveCount(0);
});

test('reads right-to-left in Persian', async ({ page }) => {
  await page.goto('/help/sell?lang=fa');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  // Persian content, not the English fallback.
  await expect(page.locator('#manual-topic')).toContainText('فروش');
  await expect(page.locator('.manual-untranslated')).toHaveCount(0);
  // The nav column mirrors to the right-hand side under RTL.
  const nav = await page.locator('.manual-nav').boundingBox();
  const panel = await page.locator('.manual-panel').boundingBox();
  expect(nav!.x).toBeGreaterThan(panel!.x);
});

// Every page carries a "?" that lands on the topic documenting THAT page.
for (const [route, topic] of [
  ['/', 'sell'],
  ['/catalog', 'catalog'],
  ['/inventory', 'inventory'],
  ['/reports', 'reports'],
] as const) {
  test(`the ? on ${route} opens the ${topic} topic`, async ({ page }) => {
    await page.goto(route);
    const hint = page.getByTestId('help-hint');
    await expect(hint).toHaveAttribute('href', `/help/${topic}`);
    await hint.click();
    await expect(page.locator('#manual-topic')).toHaveAttribute('data-topic', topic);
  });
}
