import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#325: the manual is a two-pane shell — topic tree + search on the
// inline-start side, the selected topic (rendered from embedded Markdown) on
// the other — with /help/{topic} directly linkable and correct RTL layout.

test('help manual: two-pane shell, tree navigation without reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/help?lang=en');
  await expect(page.locator('#help-tree')).toBeVisible();
  await expect(page.locator('#help-topic')).toBeVisible();

  // LTR: the tree column sits on the left of the topic panel.
  const tree = await page.locator('#help-tree').boundingBox();
  const topic = await page.locator('#help-topic').boundingBox();
  expect(tree!.x).toBeLessThan(topic!.x);

  // Clicking a topic swaps the panel and pushes the URL (htmx, no reload).
  await page.locator('a.help-topic-link[href="/help/backups"]').click();
  await expect(page.locator('#help-topic')).toContainText('Backups');
  await expect(page).toHaveURL(/\/help\/backups$/);
  assertClean();
});

test('help search filters the topic list without a page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/help?lang=en');
  // pressSequentially (not fill) so htmx's keyup trigger fires.
  await page.locator('#help-search').pressSequentially('barcode');
  await expect(
    page.locator('#help-tree a.help-topic-link', { hasText: 'Selling & checkout' }),
  ).toBeVisible();
  await expect(page.locator('#help-tree')).not.toContainText('Software updates');
  // Still the same document — no navigation happened.
  await expect(page).toHaveURL(/\/help/);
  assertClean();
});

test('direct /help/<id> link renders standalone; unknown id 404s', async ({ page }) => {
  const resp = await page.goto('/help/printing?lang=en');
  expect(resp!.status()).toBe(200);
  await expect(page.locator('#help-topic')).toContainText('printer');
  await expect(page.locator('#help-tree')).toBeVisible(); // full page, not a fragment

  const missing = await page.goto('/help/no-such-topic');
  expect(missing!.status()).toBe(404);
});

test('help manual renders RTL under fa: tree column flips sides', async ({ page }) => {
  await page.goto('/help?lang=fa');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

  const tree = await page.locator('#help-tree').boundingBox();
  const topic = await page.locator('#help-topic').boundingBox();
  // RTL: inline-start is the right side, so the tree sits to the RIGHT.
  expect(tree!.x).toBeGreaterThan(topic!.x);

  // fa has no translated topics yet — the English fallback banner shows.
  await expect(page.locator('#help-topic .help-untranslated')).toBeVisible();
});
