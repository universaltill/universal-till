import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1960: the product owner asked for Settings as "items on the left
// and open the settings on the right, and settings must be searchable". The
// page is now a two-pane master-detail — a section list (+ a search box that
// finds INDIVIDUAL settings, not just section titles) on the inline-start
// side, the selected section's card on the other. Every card is still
// rendered by the Go template on the one initial request; settings.html's
// own script only shows/hides them (no fetch, no client-side routing —
// ADR-0008 intact). These tests cover the layer Go tests can't see: the
// grid actually applying, the instant switch, both real inbound deep links
// (/settings#registration from base.html, /settings#android-update from
// update_api.go — ut-docs#1534's own scroll fix must not regress), the
// search, and the phone-width list-first degradation.

// The status chip's Download link only renders on an Android till with an
// update available; the e2e till is neither, so the #android-update anchor
// (a <span> nested inside the Software update card's install form) is not
// in the DOM here. The nested-anchor resolution it depends on is exercised
// with another nested id from the same section instead, and the Go test
// settings_two_pane_test.go pins that the anchor itself renders inside
// id="settings-update" on an Android till.
const NESTED_IN_UPDATE_CARD = 'update-check-msg';

test.describe('settings two-pane layout (ut-docs#1960)', () => {
  test('no hash: first section shown on the panel, several sections listed, real grid', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');

    await expect(page.locator('#settings-shell')).toHaveCSS('display', 'grid');
    const items = page.locator('#settings-tree a[data-section]');
    expect(await items.count(), 'the section list is built from the rendered cards').toBeGreaterThan(10);

    // First card in DOM order is the registration card; it is the one shown.
    await expect(page.locator('#registration')).toBeVisible();
    await expect(items.first()).toHaveAttribute('aria-current', 'page');
    await expect(items.first()).toHaveClass(/is-current/);
    // Everything else is hidden, but still IN the DOM (server-rendered once).
    await expect(page.locator('#settings-display')).toBeHidden();
    await expect(page.locator('#settings-display')).toBeAttached();
    const shown = await page.locator('#settings-grid > .card:not(.settings-section-hidden)').count();
    expect(shown, 'exactly one section visible at a time').toBe(1);

    // Side by side, not stacked (mirrors manual.spec.ts's ut-docs#389 check).
    const nav = (await page.locator('#settings-nav').boundingBox())!;
    const panel = (await page.locator('#settings-panel').boundingBox())!;
    expect(nav.x + nav.width).toBeLessThanOrEqual(panel.x + 1);
    // Tablet+ width: the phone-only back control is not shown.
    await expect(page.locator('#settings-back')).toBeHidden();
    assertClean();
  });

  test('clicking a section switches instantly, without a page load', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    // A marker that a full navigation would wipe.
    await page.evaluate(() => ((window as any).__ut1960 = 'alive'));

    await page.locator('#settings-tree a[data-section="settings-display"]').click();
    await expect(page.locator('#settings-display')).toBeVisible();
    await expect(page.locator('#registration')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="settings-display"]')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#settings-tree a[data-section="registration"]')).not.toHaveAttribute('aria-current', 'page');
    // replaceState keeps the URL shareable without a reload.
    await expect(page).toHaveURL(/\/settings#settings-display$/);
    expect(await page.evaluate(() => (window as any).__ut1960), 'no full page navigation happened').toBe('alive');
    // Focus moved to the section heading for keyboard/screen-reader users.
    await expect(page.locator('#settings-display h2')).toBeFocused();
    assertClean();
  });

  test('/settings#registration deep link (base.html) opens the registration section', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    // Arrive from a different page so the hash is a real inbound link.
    await page.goto('/settings#settings-printer');
    await expect(page.locator('#settings-printer')).toBeVisible();
    await page.goto('/settings#registration');
    await expect(page.locator('#registration')).toBeVisible();
    await expect(page.locator('#settings-printer')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="registration"]')).toHaveAttribute('aria-current', 'page');
    assertClean();
  });

  test('a hash naming an element NESTED in a card opens that card and brings the element into view', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 600 });
    await page.goto(`/settings#${NESTED_IN_UPDATE_CARD}`);
    // The nested id resolves to its enclosing card — the Software update
    // section — exactly as /settings#android-update does on an Android till.
    await expect(page.locator('#settings-update')).toBeVisible();
    await expect(page.locator('#registration')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="settings-update"]')).toHaveAttribute('aria-current', 'page');
    const target = page.locator(`#${NESTED_IN_UPDATE_CARD}`);
    await expect(target).toBeAttached();
    // In the viewport after the post-show re-scroll (ut-docs#1534 / #1960).
    await expect
      .poll(async () => {
        const box = await target.boundingBox();
        const vh = page.viewportSize()!.height;
        return !!box && box.y >= 0 && box.y <= vh;
      }, 'the nested anchor must end up inside the viewport')
      .toBe(true);
    assertClean();
  });

  test('search finds an individual setting in a non-first section and opens it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    await expect(page.locator('#registration')).toBeVisible();

    // A control label from the Receipt printer section (settings.printer
    // .discover.find_button), matched case-insensitively.
    await page.locator('#settings-q').fill('find printers');
    const hits = page.locator('#settings-tree a[data-hit]');
    await expect(hits.first()).toBeVisible();
    await expect(hits.first().locator('.settings-hit-text')).toContainText('Find printers on this network');
    // The result names the section it lives in.
    await expect(hits.first().locator('.settings-hit-section')).toContainText('Receipt printer');
    // The plain section list is replaced while a query is active.
    await expect(page.locator('#settings-tree a[data-section]:not([data-hit])')).toHaveCount(0);

    // A <label> that wraps its own <select> (e.g. the Printer section's
    // "Connection" label around <select name="mode">) must index as just
    // its own words, not glued to every one of that dropdown's <option>
    // texts — buildIndex() strips select/option/textarea descendants for
    // exactly this. Search for the label's own word and assert the hit
    // text is the bare label, not "Connection Off Network (IP) USB
    // device ...". Restore the original query afterward — `hits` below is
    // a live locator re-evaluated at click time, so it must still resolve
    // against "find printers"' results, not this query's.
    await page.locator('#settings-q').fill('connection');
    const connectionHit = page.locator('#settings-tree a[data-hit]').filter({ hasText: 'Connection' });
    await expect(connectionHit.first().locator('.settings-hit-text')).toHaveText('Connection');
    await page.locator('#settings-q').fill('find printers');
    await expect(hits.first().locator('.settings-hit-text')).toContainText('Find printers on this network');

    await hits.first().click();
    await expect(page.locator('#settings-printer')).toBeVisible();
    await expect(page.locator('#registration')).toBeHidden();
    await expect(page.locator('#printer-discover-btn')).toBeInViewport();
    await expect(page.locator('#printer-discover-btn')).toHaveClass(/settings-hit-target/);

    // No match: the list hides and the empty state shows.
    await page.locator('#settings-q').fill('zzzz-no-such-setting-1960');
    await expect(page.locator('#settings-noresults')).toBeVisible();
    await expect(page.locator('#settings-tree')).toBeHidden();

    // Clearing restores the per-section list with the selection kept.
    await page.locator('#settings-q').fill('');
    await expect(page.locator('#settings-noresults')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section]:not([data-hit])').first()).toBeVisible();
    await expect(page.locator('#settings-tree a[data-section="settings-printer"]')).toHaveAttribute('aria-current', 'page');
    assertClean();
  });

  test('phone width, no hash: list first, tap opens the section, back returns to the list', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 375, height: 740 });
    await page.goto('/settings');

    await expect(page.locator('#settings-nav')).toBeVisible();
    await expect(page.locator('#settings-panel')).toBeHidden();
    await expect(page.locator('#settings-shell')).not.toHaveClass(/has-selection/);

    await page.locator('#settings-tree a[data-section="settings-currency"]').click();
    await expect(page.locator('#settings-panel')).toBeVisible();
    await expect(page.locator('#settings-currency')).toBeVisible();
    await expect(page.locator('#settings-nav')).toBeHidden();
    const back = page.locator('#settings-back');
    await expect(back).toBeVisible();

    await back.click();
    await expect(page.locator('#settings-nav')).toBeVisible();
    await expect(page.locator('#settings-panel')).toBeHidden();
    assertClean();
  });

  test('phone width WITH a hash: the deep-linked section shows directly', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 375, height: 740 });
    await page.goto('/settings#settings-display');
    await expect(page.locator('#settings-panel')).toBeVisible();
    await expect(page.locator('#settings-display')).toBeVisible();
    await expect(page.locator('#settings-back')).toBeVisible();
    assertClean();
  });

  test('every rendered card is listed as a section — nothing became unreachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    const cardIds = await page.locator('#settings-grid > .card').evaluateAll((els) => els.map((el) => el.id));
    const navIds = await page.locator('#settings-tree a[data-section]').evaluateAll((els) => els.map((el) => el.getAttribute('data-section')));
    expect(cardIds.length).toBeGreaterThan(10);
    expect(cardIds.every((id) => id !== ''), 'every card carries an id').toBe(true);
    expect(navIds).toEqual(cardIds);
    assertClean();
  });
});
