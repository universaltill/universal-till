import { test, expect } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// The OSK mode is a SERVER-side setting shared by every spec on this server
// (see settings-osk.spec.ts) — restore 'auto' even on failure.
test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

// ut-docs#196: the Designer's product search box used hx-trigger="keyup",
// but the on-screen keyboard (web/public/osk.js) types by tapping virtual
// keys, which only ever dispatches a synthetic "input" event — never a
// native keydown/keyup. So on any touch till (OSK forced/auto-detected on),
// typing a product name into the search box showed no results at all,
// though the exact same query worked fine from a real keyboard. Drive the
// OSK for real (tap keys, no keyboard events) and confirm results render —
// the class of bug a Go handler test alone can't see, since the backend
// search was never broken.
test('Designer search box returns results when typed via the on-screen keyboard', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/designer');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');

  const search = page.locator('#search');
  await search.click();
  await expect(page.locator('#osk')).toBeVisible();

  // Demo-seeded catalog item (001_init.sql), used elsewhere in this suite
  // (catalog-image-to-till.spec.ts) — untouched by any other spec.
  for (const k of ['s', 'p', 'a']) {
    await page.locator(`#osk button[data-k="${k}"]`).click();
  }
  await expect(search).toHaveValue('spa');

  await expect(page.locator('#search-results')).toContainText('Sparkling Water', { timeout: 5000 });

  assertClean();
});

// Companion coverage: a real keyboard must keep working exactly as before —
// this fix widens the trigger's event, it must not narrow it.
test('Designer search box returns results when typed on a real keyboard', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.goto('/designer');
  const search = page.locator('#search');
  await search.pressSequentially('spa', { delay: 20 });

  await expect(page.locator('#search-results')).toContainText('Sparkling Water', { timeout: 5000 });

  assertClean();
});
