import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#917: the item form's "Saved" notice was rendered and then wiped in
// the same synchronous tick on the NEW-item path, so the operator never saw
// any confirmation at all — the one path where they most need it.
//
//   .then(function () { renderNotice(msg, 'success', …); if (!editing) clearForm(); … })
//
// clearForm() unconditionally blanks #item-form-msg (that is what the "＋ New"
// reset button wants it to do), so on `!editing` it destroyed the notice that
// had just been put there, before the browser ever painted a frame. The fix is
// the ordering: clear first, then render.
//
// This asserts the notice is actually VISIBLE and STAYS visible for a real
// slice of its auto-expire window (app.js's scheduleToastDismiss starts hiding
// a non-error .pos-notice at 2500ms), not merely that the handler ran — a
// test that only checked "renderNotice was called" passed against the bug.
test.describe('catalog item-form save notice (ut-docs#917)', () => {
  const msgSel = '#item-form-msg';

  async function fillNewItem(page: import('@playwright/test').Page, name: string) {
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.50');
  }

  test('saving a NEW item shows a "Saved" notice that survives to be seen', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const msg = page.locator(msgSel);
    await expect(msg).toBeEmpty();

    const name = 'Notice Probe ' + Date.now();
    await fillNewItem(page, name);
    await page.locator('#item-form-submit').click();

    // The success notice must be present AND visible — the bug left the slot
    // empty, so `toBeEmpty()` was true here.
    const notice = msg.locator('.pos-notice.success');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('Saved');

    // …and it must still be there well inside the 2500ms auto-expire window.
    // The old code's wipe was synchronous, so a notice that is still visible
    // a full second later could not have been cleared by clearForm().
    await page.waitForTimeout(1000);
    await expect(notice).toBeVisible();

    // The form still resets on the new-item path — the fix reorders the two
    // steps, it does not drop clearForm().
    await expect(page.locator('#item-name')).toHaveValue('');
    await expect(page.locator('#item-price')).toHaveValue('');
    await expect(page.locator('#item-id')).toHaveValue('');

    // And the item really was created (the notice is not lying).
    await expect(page.locator('#catalog-table')).toContainText(name);

    assertClean();
  });

  test('saving an EDIT to an existing item still shows its notice', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // Create one, then edit it — the edit path never called clearForm(), so it
    // was unaffected by the bug; this guards against the fix regressing it.
    const name = 'Notice Probe Edit ' + Date.now();
    await fillNewItem(page, name);
    await page.locator('#item-form-submit').click();
    await expect(page.locator(msgSel).locator('.pos-notice.success')).toBeVisible();

    // Click a plain cell, not the row's centre — the row-click handler
    // deliberately ignores clicks that land on a `.btn` inside it.
    await page.locator('#catalog-table .catalog-row', { hasText: name }).first()
      .locator('td').first().click();
    await expect(page.locator('#item-id')).not.toHaveValue('');

    await page.locator('#item-description').fill('edited');
    await page.locator('#item-form-submit').click();

    const notice = page.locator(msgSel).locator('.pos-notice.success');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('Saved');
    // Editing deliberately keeps the form populated for further edits.
    await expect(page.locator('#item-name')).toHaveValue(name);

    assertClean();
  });

  // Independent review of the first version of this fix found it introduced
  // an active regression: htmx.ajax()'s returned promise resolves on ANY
  // completed HTTP response — including the server's real 400s (duplicate
  // SKU, bad category/brand/tax, invalid autofill barcode) — not just 2xx.
  // A naive reorder that unconditionally ran `clearForm()` + the success
  // notice inside `.then()` would wipe the real error the
  // htmx:responseError listener had just rendered, clear out everything the
  // operator typed, and paint a false "Saved" over an item that was never
  // created. This drives that exact failure (duplicate SKU) and asserts
  // none of that happens.
  test('a FAILED new-item save shows the real error, not a false "Saved", and keeps the form', async ({ page }) => {
    // Two distinct console.error lines come out of this deliberately-
    // triggered 400, neither a JS bug: the browser's own resource-load
    // failure line, and htmx's own "Response Status Error Code …" (fired
    // from fe(...,"htmx:responseError",...) in the vendored htmx.min.js) —
    // same reasoning as htmx-admin-error-swap-916.spec.ts's own
    // watchConsole exemptions.
    const assertClean = watchConsole(
      page,
      /^(Failed to load resource:.*400.*|Response Status Error Code 400 from \/api\/catalog\/item)$/,
    );
    await page.goto('/catalog');

    const sku = 'DUP-' + Date.now();
    const first = 'Notice Probe Dup A ' + Date.now();
    await fillNewItem(page, first);
    await page.locator('#item-sku').fill(sku);
    await page.locator('#item-form-submit').click();
    await expect(page.locator(msgSel).locator('.pos-notice.success')).toBeVisible();

    // Second item, same SKU — the server rejects this with 400
    // catalog.error.sku_exists (skuAwareError, handlers.go).
    const second = 'Notice Probe Dup B ' + Date.now();
    await fillNewItem(page, second);
    await page.locator('#item-sku').fill(sku);
    await page.locator('#item-form-submit').click();

    const msg = page.locator(msgSel);
    const errorNotice = msg.locator('.pos-notice.error');
    await expect(errorNotice).toBeVisible();
    await expect(errorNotice).toContainText('already in use');
    // Never a false success alongside/after the error.
    await expect(msg.locator('.pos-notice.success')).toHaveCount(0);

    // The form must NOT have been cleared — the operator's typed name/SKU
    // are still there to fix and resubmit, not silently discarded.
    await expect(page.locator('#item-name')).toHaveValue(second);
    await expect(page.locator('#item-sku')).toHaveValue(sku);

    // And the second item was genuinely never created.
    await expect(page.locator('#catalog-table')).not.toContainText(second);

    assertClean();
  });
});
