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

// ut-docs#1170: `.btn` (the search-result "add" button's own class, among
// 306+ others) carried no `user-select`/`touch-action` of its own — the only
// place those existed was gated behind `body.kiosk` (app.css), which never
// applies on a windowed desktop-shell install (ut-docs#1021's own confirmed
// hardware finding: kiosk service inactive, display.window_mode=normal on
// the reporting till). This is the actual, provable regression: assert the
// protection now applies UNCONDITIONALLY, not just under body.kiosk. HONESTY
// NOTE: confirmed empirically (not assumed) that a synthetic Playwright
// `locator.tap()` on a plain `<button>` does NOT reproduce the real
// WebKitGTK text-selection-swallow bug — this exact behavioural test below
// (tap the result, assert the item got added) PASSES even against the
// unfixed CSS, so it is kept as documentation of the intended user flow, not
// as proof of this fix. The computed-style assertion is the one that
// actually goes red pre-fix and green post-fix — same "prove the CSS
// scoping, not real touch hardware" honesty pattern
// tables-tap-to-add-1025.spec.ts already established for this exact class of
// bug.
test('search-result add button has user-select/touch-action protection outside kiosk mode (ut-docs#1170)', async ({
  page,
}) => {
  const assertClean = watchConsole(page);
  await page.goto('/designer');
  await expect(page.locator('body')).not.toHaveClass(/kiosk/);

  const search = page.locator('#search');
  await search.pressSequentially('spa', { delay: 20 });
  const result = page.locator('#search-results .result', { hasText: 'Sparkling Water' });
  await expect(result).toBeVisible({ timeout: 5000 });

  const style = await result.evaluate((el) => {
    const s = getComputedStyle(el);
    return { userSelect: s.userSelect, touchAction: s.touchAction };
  });
  expect(style.userSelect).toBe('none');
  expect(style.touchAction).toBe('manipulation');

  assertClean();
});

// Documentation of the intended user-facing flow (see honesty note above) —
// kept because it's still real, valid coverage of the tap-to-add mechanism
// itself, just not evidence for this specific CSS fix.
test('Designer search result tap-to-add works from a touch context (ut-docs#1170)', async ({ browser }) => {
  const ctx = await browser.newContext({ hasTouch: true });
  const page = await ctx.newPage();
  const assertClean = watchConsole(page);

  await page.goto('/designer');
  const tiles = page.locator('#buttons-grid-admin .tile-name', { hasText: 'Sparkling Water' });
  // Count-based, not visibility-based: the shared dev till server persists
  // added tiles across repeated local runs (reuseExistingServer), so a
  // previous run may have already added this item — assert the tap adds
  // ONE MORE, not that it's the first ever.
  const before = await tiles.count();

  const search = page.locator('#search');
  await search.pressSequentially('spa', { delay: 20 });

  const result = page.locator('#search-results .result', { hasText: 'Sparkling Water' });
  await expect(result).toBeVisible({ timeout: 5000 });
  await result.tap();

  await expect(tiles).toHaveCount(before + 1, { timeout: 5000 });

  assertClean();
  await ctx.close();
});
