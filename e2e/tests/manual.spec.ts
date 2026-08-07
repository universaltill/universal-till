import { test, expect } from '@playwright/test';

// The user manual (/help): the layer Go tests can't see — the htmx swap when
// you pick a topic, the debounced search, the RTL mirror, and the contextual
// "?" actually resolving from a real page.

test('manual lists topics and opens one without a full page load', async ({ page }) => {
  await page.goto('/help');
  await expect(page.locator('.manual-nav')).toBeVisible();
  await expect(page.locator('.manual-panel')).toBeVisible();
  await expect(page.locator('.manual-link').first()).toBeVisible();
  // ut-docs#389: `.manual-nav`/`.manual-panel` both being present in the DOM
  // (the assertions above) does NOT prove the two-pane layout actually
  // rendered — Playwright's toBeVisible() only checks display/visibility,
  // not whether app.css loaded at all. A CSS-load failure leaves both
  // elements individually "visible" but stacked in one unstyled column, the
  // exact regression reported live. Assert real side-by-side geometry too:
  // the nav's box must end (its inline-end edge) at or before the panel's
  // inline-start edge, which only holds under the `.manual { display: grid;
  // grid-template-columns: ... }` rule from app.css.
  const nav = await page.locator('.manual-nav').boundingBox();
  const panel = await page.locator('.manual-panel').boundingBox();
  expect(nav!.x + nav!.width).toBeLessThanOrEqual(panel!.x + 1); // +1: sub-pixel rounding

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
// /backoffice and /menu are the ut-docs#326 coverage additions: pages the
// route-coverage guard found undocumented.
for (const [route, topic] of [
  ['/', 'sell'],
  ['/catalog', 'catalog'],
  ['/inventory', 'inventory'],
  ['/reports', 'reports'],
  ['/backoffice', 'alerts'],
  ['/menu', 'menu'],
] as const) {
  test(`the ? on ${route} opens the ${topic} topic`, async ({ page }) => {
    await page.goto(route);
    // .first(): a page may carry extra section-level helpLink hints besides
    // the nav's automatic one; the nav "?" always renders first.
    const hint = page.getByTestId('help-hint').first();
    await expect(hint).toHaveAttribute('href', `/help/${topic}`);
    await hint.click();
    await expect(page.locator('#manual-topic')).toHaveAttribute('data-topic', topic);
  });
}

// A parameterized route: reports.md declares /journal/{receipt}, and the "?"
// on a REAL receipt detail page (whose concrete path never equals that
// pattern string) must resolve through the segment-wise matcher — the
// end-to-end proof the Go unit tests can't give. The receipt comes from
// completing a real cash sale, the same flow sale.spec.ts drives; the e2e
// till has no pre-seeded sales.
test('the ? on a receipt detail page opens the reports topic', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();

  await page.goto('/journal');
  const receiptLink = page.locator('a[href^="/journal/"]').first();
  await expect(receiptLink).toBeVisible();
  await receiptLink.click();
  await expect(page).toHaveURL(/\/journal\/.+/);

  const hint = page.getByTestId('help-hint').first();
  await expect(hint).toHaveAttribute('href', '/help/reports');
  await hint.click();
  await expect(page.locator('#manual-topic')).toHaveAttribute('data-topic', 'reports');
});

// The settings page's route is claimed by the display topic, but five topics
// document SECTIONS of it — those carry explicit helpLink "?" hints next to
// their headings (backups, claim, payments, printing, updates). Asserting
// each renders and points at its topic; no bounding-box placement check —
// settings isn't a tap-critical kiosk surface like the tender panel, so
// existence + target is the right strength here.
test('settings sections carry their own ? hints', async ({ page }) => {
  await page.goto('/settings');
  for (const topic of ['claim', 'updates', 'payments', 'backups', 'printing']) {
    await expect(
      page.locator(`a.help-hint[href="/help/${topic}"]`),
      `settings section ? for ${topic}`,
    ).toBeVisible();
  }
});
