import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2179 (split out of ut-docs#2162/#2116's independent review): a
// 403/500 from a destination handler mid in-panel swap on /admin or /items
// was a silent no-op. Two things were wrong at once:
//
// 1. `httpx.RenderError` (internal/httpx/render_error.go) has no htmx
//    awareness — it always writes a full `base`-templated HTML document
//    with a non-2xx status, even when the request that hit it was an
//    htmx in-panel fragment GET. app.js's own htmx:beforeSwap listener
//    (ut-docs#916) force-swaps ANY non-2xx text/html, non-empty response
//    into the swap target — which wrongly captured this full document too,
//    same as the `.muted` fragments #916 actually means to catch. Forcing
//    a whole `<html><head>...<body>...` document into a small panel div
//    as innerHTML doesn't render anything sane (a template-parsed full
//    document loses its meaningful content) — the exact "does nothing at
//    all" this card reports.
// 2. Even where htmx:responseError DOES fire (once (1) stops swallowing
//    it), showAlert()'s target (`#pos-alert`) never existed on admin.html/
//    items.html at all — only on the sale screen (index.html) — so the
//    existing app-wide handler had nothing to show the message in.
//
// Both fixes are purely client-side + template (no Go handler change), so
// this is mocked at the network layer via page.route() — same "test hook"
// shape htmx-admin-error-swap-916.spec.ts and htmx-senderror-1287.spec.ts
// already use for this class of client-swap-handling bug, deterministic
// rather than depending on a real backend failure condition.
test.describe('admin/items in-panel swap failures show a visible banner, not a silent no-op (ut-docs#2179)', () => {
  test('/admin: a 500 during a tree-row swap shows #pos-alert and leaves the panel untouched', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*500|^Response Status Error Code 500/);
    await page.goto('/admin');
    await expect(page.locator('#admin-tree')).toBeVisible();

    const panel = page.locator('#admin-panel');
    // /admin embeds the first fragment-capable destination by default
    // (admin_page.go's firstFragmentCapableHref) — Locations, in this
    // fixture's seeded permission set — so this heading is the stable
    // "still the original content" marker below. Not a raw innerHTML
    // snapshot: osk.js mutates form-field attributes (inputmode="none"
    // guards, ut-docs#1329) asynchronously after render, unrelated to
    // this fix, which made an exact before/after HTML comparison flaky.
    await expect(panel.locator('h1')).toHaveText('Locations');

    const alert = page.locator('#pos-alert');
    await expect(alert).toBeHidden();

    // A real httpx.RenderError response shape: full document, non-2xx,
    // Content-Type text/html, non-empty body — exactly what the old
    // beforeSwap logic force-swapped.
    await page.route('**/country-settings', (route) =>
      route.fulfill({
        status: 500,
        contentType: 'text/html; charset=utf-8',
        body: '<!doctype html><html><head><title>Error</title></head><body><nav>x</nav><main><h1>Error</h1><p>Something went wrong. Please try again.</p></main></body></html>',
      }),
    );
    await page.locator('.items-row[href="/country-settings"]').click();

    await expect(alert).toBeVisible();
    await expect(alert).toContainText('Something went wrong');
    // The panel is untouched — not wiped, and not replaced with the fetched
    // document's own unrelated markup (its <h1>Error</h1> must not appear).
    await expect(panel.locator('h1')).toHaveText('Locations');
    await expect(panel.getByText('Error', { exact: true })).toHaveCount(0);

    // Dismissible, per AC #2.
    await alert.locator('.notice-dismiss').click();
    await expect(alert).toBeHidden();

    assertClean();
  });

  test('/items: a 403 during a rail-row swap shows #pos-alert and leaves the panel untouched', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*403|^Response Status Error Code 403/);
    await page.goto('/items');
    await expect(page.locator('#items-rail')).toBeVisible();

    const panel = page.locator('#items-panel');
    // /items embeds its first section (Catalog) by default.
    await expect(panel.locator('h1')).toHaveText('Catalog');

    const alert = page.locator('#pos-alert');
    await expect(alert).toBeHidden();

    await page.route('**/inventory', (route) =>
      route.fulfill({
        status: 403,
        contentType: 'text/html; charset=utf-8',
        body: '<!doctype html><html><head><title>Error</title></head><body><nav>x</nav><main><h1>Error</h1><p>Not allowed.</p></main></body></html>',
      }),
    );
    await page.locator('.items-row[href="/inventory"]').click();

    await expect(alert).toBeVisible();
    await expect(alert).toContainText('Something went wrong');
    await expect(panel.locator('h1')).toHaveText('Catalog');
    await expect(panel.getByText('Not allowed.')).toHaveCount(0);

    assertClean();
  });
});
