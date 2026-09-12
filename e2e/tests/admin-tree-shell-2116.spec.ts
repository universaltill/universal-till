import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2116: the /admin tree used to be plain full-page <a href>
// navigation to six standalone pages (deliberately, per ut-docs#2008) —
// clicking a node lost the tree entirely and landed on "a new page on the
// whole screen". This mirrors the /items rail pattern (ut-docs#1950)
// exactly: the tree stays in a left pane, a click swaps content into
// #admin-panel via htmx, and the clicked node is marked is-current via an
// out-of-band refresh of the tree partial.
//
// Stronger than /items' own precedent: a bare, direct GET to any of the
// six destination URLs (not just a click from /admin) also renders inside
// the shell with the correct node pre-selected — selection is derived
// from the request route on every render, not only from client-side click
// state. That's the main new mechanism this card adds, so it gets its own
// dedicated assertion below rather than just being implied by the swap
// test.
test.describe('/admin tree: two-pane shell (ut-docs#2116)', () => {
  test('wide viewport: clicking a tree node swaps the panel in place and marks it current', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/admin');

    await expect(page.locator('#admin-tree')).toBeVisible();
    // Bare /admin: no destination selected yet, empty-state placeholder shown.
    await expect(page.locator('#admin-panel')).toBeVisible();
    await expect(page.locator('.admin-tree .items-row.is-current')).toHaveCount(0);

    const locationsRow = page.locator('#admin-tree a[href="/locations"]');
    await expect(locationsRow).toBeVisible();

    // Review finding (ut-docs#2116): at this width the destination ALSO
    // satisfies every URL/DOM assertion below via a full page navigation
    // (this PR's own bare-GET shell rendering makes both paths converge on
    // the same visible state), so those assertions alone cannot prove the
    // click was a real in-place htmx swap rather than a reload. A sentinel
    // set before the click, checked after, is what actually distinguishes
    // them: it survives an htmx swap (the document is never torn down) and
    // is wiped by any real navigation.
    await page.evaluate(() => { (window as unknown as { __navSentinel?: number }).__navSentinel = 1; });
    await locationsRow.click();

    // A real htmx in-panel swap, not a full navigation away from the shell.
    await expect(page).toHaveURL(/\/locations$/);
    expect(
      await page.evaluate(() => (window as unknown as { __navSentinel?: number }).__navSentinel),
      'the sentinel must survive an in-place htmx swap — a full navigation would tear down the document and lose it',
    ).toBe(1);
    await expect(page.locator('#admin-tree')).toBeVisible();
    await expect(locationsRow).toHaveClass(/is-current/);
    await expect(locationsRow).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#admin-panel')).toContainText(/./); // real content rendered, not blank

    // Side by side, not stacked, at this width.
    const tree = (await page.locator('.admin-tree-wrap').boundingBox())!;
    const panel = (await page.locator('#admin-panel').boundingBox())!;
    expect(tree.x + tree.width).toBeLessThanOrEqual(panel.x + 1);
    assertClean();
  });

  test('direct/deep link to a destination renders inside the shell with the correct node selected', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    // Going straight to /translations (never visiting /admin first) must
    // still show the tree, with Translations marked current — this is
    // stronger than /items' own precedent (a direct /catalog visit shows
    // no rail at all).
    await page.goto('/translations');

    await expect(page.locator('#admin-tree')).toBeVisible();
    const translationsRow = page.locator('#admin-tree a[href="/translations"]');
    await expect(translationsRow).toHaveClass(/is-current/);
    await expect(translationsRow).toHaveAttribute('aria-current', 'page');

    // A reload preserves it (selection is route-derived, not client state).
    await page.reload();
    await expect(page.locator('#admin-tree')).toBeVisible();
    await expect(page.locator('#admin-tree a[href="/translations"]')).toHaveClass(/is-current/);
    assertClean();
  });

  test('phone width: tapping a tree node falls back to a plain navigation, not a stuck swap', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 375, height: 740 });
    await page.goto('/admin');
    await expect(page.locator('#admin-tree')).toBeVisible();

    const registersRow = page.locator('#admin-tree a[href="/registers"]');

    // Same review finding as the wide-viewport test above: hx-push-url
    // means an htmx swap ALSO lands on /registers, so the URL alone can't
    // tell a real navigation apart from a swap. The sentinel is wiped by a
    // real navigation and survives a swap — here we assert it's GONE.
    await page.evaluate(() => { (window as unknown as { __navSentinel?: number }).__navSentinel = 1; });
    await registersRow.click();

    // Real navigation to the destination's own full URL (the href
    // fallback), not an htmx swap staying "stuck" on /admin at a width
    // where the two-pane layout has stacked and a swap into an
    // off-screen/hidden panel would look broken.
    await expect(page).toHaveURL(/\/registers$/);
    expect(
      await page.evaluate(() => (window as unknown as { __navSentinel?: number }).__navSentinel),
      'a real plain navigation tears down the document and loses the sentinel — an htmx swap would not',
    ).toBeUndefined();
    assertClean();
  });
});
