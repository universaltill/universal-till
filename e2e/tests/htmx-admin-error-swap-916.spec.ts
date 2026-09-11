import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#916: htmx never swaps a non-2xx response into its target by
// default (it fires htmx:responseError and discards the body instead).
// Admin-page handlers that fail (print/labels, print/test, invoice issue,
// backup, sync join/promote, …) still render a real, translated `.muted`
// error fragment straight into their own hx-target — but with no fix, that
// fragment is silently discarded and the operator sees nothing at all.
// app.js's htmx:beforeSwap listener now forces the swap for every such
// handler outside the sale screen's own #pos-alert carve-out (ut-docs#213).
//
// Two independent endpoints/pages, on purpose — this is a generic client-
// side fix, not a per-endpoint one, so one instance passing wouldn't prove
// the fix is actually general.
test.describe('admin htmx error fragments are shown, not silently dropped (ut-docs#916)', () => {
  test('print/test failure (502) shows a visible error on settings.html', async ({ page }) => {
    // The disabled-printer 502 this test deliberately triggers logs its own
    // "Failed to load resource: … 502" browser-level console error — expected
    // noise, not a JS bug (see helpers.ts's watchConsole doc comment).
    const assertClean = watchConsole(page, /^Failed to load resource:.*502/);
    await page.goto('/settings#settings-printer'); // ut-docs#1960: Settings is two-pane now — deep-link to the section this drives
    // No printer is configured in the e2e fixture (printer.mode defaults to
    // "off"), so this deterministically hits print_api.go's BadGateway path.
    const msg = page.locator('#print-test-msg');
    await expect(msg).toBeEmpty();
    await page.getByRole('button', { name: 'Print test receipt' }).click();
    await expect(msg).not.toBeEmpty();
    await expect(msg).toContainText('Print failed');
    assertClean();
  });

  test('print/labels failure (404, unknown item) shows a visible error on catalog.html', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    // ut-docs#1901: the labels form moved inside the item-form dialog,
    // which starts closed. ut-docs#1956: it is the dialog's "Print labels"
    // tab now (no more <details>), and in create mode that tab shows a
    // save-first hint with the form hidden — open an existing item
    // instead so the form is really on screen.
    await page.locator('.catalog-row', { hasText: 'Sugar 1kg' }).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-form-tab-labels').click();
    await expect(page.locator('#labels-item-id')).toHaveValue(/.+/);
    // Bypass the picker UI and drive the hidden field directly — the bug
    // is in the client-side swap handling, not the picker.
    await page.locator('#labels-item-id').evaluate((el: HTMLInputElement) => {
      el.value = 'no-such-item-id';
    });
    const msg = page.locator('#labels-msg');
    await expect(msg).toBeEmpty();
    await page.locator('form:has(#labels-item-id) button[type="submit"]').click();
    await expect(msg).not.toBeEmpty();
    await expect(msg).toContainText('Pick an item first');
    assertClean();
  });

  // Independent review of the first version of this fix (which force-swapped
  // ANY non-2xx response) found this would have been an active regression:
  // some handlers answer a bad request with a plain http.Error(...) — which
  // is text/plain, not the translated .muted text/html fragment the fix is
  // meant to surface. htmx's fragment parser yields zero nodes for a plain-
  // text body against an outerHTML target, so force-swapping it would WIPE
  // the target instead of showing anything — worse than the original bug.
  // /api/catalog/barcode/delete's "barcode required" 400 (catalog/handlers.go)
  // is a real, live instance of exactly that shape, targeting
  // #catalog-variants with hx-swap="outerHTML" — driven directly via
  // htmx.ajax (same real endpoint + swap config the shipped delete button
  // uses), using an item with no existing variants (Sugar 1kg / itm050) so
  // catalog.html's own htmx:configRequest listener — which stamps a `price`
  // parameter onto ANY htmx request whenever a `.variant-price-major` field
  // exists anywhere on the page, unrelated to this endpoint — can't
  // accidentally satisfy a *different* validation path instead of the one
  // this test means to hit.
  test('a plaintext (non-fragment) error response never wipes its swap target', async ({ page }) => {
    // This 400 is deliberately left un-swapped (it's text/plain, not the
    // .muted fragment convention this fix targets) — htmx's own default,
    // unhandled-error path logs both the browser's generic resource-load
    // message and its own "Response Status Error Code …" line. Both are
    // expected here, not a JS bug.
    const assertClean = watchConsole(page, /^Failed to load resource:.*400|^Response Status Error Code 400/);
    await page.goto('/catalog');
    await page.locator('.catalog-row', { hasText: 'Sugar 1kg' }).click();
    // ut-docs#1956: the panel is the item dialog's Variants tab now.
    await page.locator('#item-form-tab-variants').click();
    const panel = page.locator('#catalog-variants');
    await expect(panel.locator('.catalog-detail-grid')).toBeVisible();
    const before = await panel.innerHTML();
    expect(before.length).toBeGreaterThan(0);

    await page.evaluate(() => {
      // @ts-expect-error htmx is a global loaded by base.html, not typed here.
      return htmx.ajax('POST', '/api/catalog/barcode/delete', {
        target: '#catalog-variants',
        swap: 'outerHTML',
        values: { panelItem: 'itm050' }, // no barcode -> 400 "barcode required"
      });
    });

    // The panel must still be there with its real content — not emptied,
    // not replaced by nothing, not removed from the DOM entirely.
    await expect(panel).toBeVisible();
    const after = await panel.innerHTML();
    expect(after.length).toBeGreaterThan(100);
    assertClean();
  });
});
