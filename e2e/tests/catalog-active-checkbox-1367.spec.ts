import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#1367: the item-edit form's "Active" checkbox had no paired hidden
// isActive=0 fallback (unlike the variant/modifier-group forms, which
// already had this) — an unchecked HTML checkbox submits NOTHING for its
// field name, so the server read the field's absence as "still active" and
// unchecking Active + Save silently did nothing. The request returned 200
// with no visible error, so an operator had no way to tell the deactivation
// didn't take.
//
// This drives the real browser checkbox, not just a synthetic POST body —
// the bug is specifically about what an unchecked <input type="checkbox">
// contributes to a real form submission.
test('unchecking Active on an existing item and saving actually deactivates it', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/catalog');

  const name = 'Active Checkbox Probe ' + Date.now();
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('2.00');
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
  // ut-docs#1901: close the create dialog explicitly. Not about inertness
  // — the dialog is opened NON-modally (.show(), ut-docs#1385's OSK fix),
  // so nothing outside it is inert — but about plain stacking: it's a
  // large `position: fixed` box (z-index 500) covering most of the
  // viewport, so it intercepts the pointer for the row click below.
  // Relying on the save-success auto-close timer instead would make this
  // test's timing depend on an implementation detail it isn't testing.
  await page.locator('#item-form-close-btn').click();

  const row = page.locator('.catalog-row', { hasText: name });
  await expect(row).toBeVisible();

  // Load it into the form — ut-docs#1951: the row is a single clickable
  // card now, not a table row of cells. The Active box starts checked,
  // since only active items ever have a card to click.
  await row.click();
  await expect(page.locator('#item-id')).not.toHaveValue('');
  await expect(page.locator('#item-active')).toBeChecked();

  await page.locator('#item-active').uncheck();
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

  // The row-level OOB response for a newly-inactive item is a delete
  // fragment (ut-docs#1363) — inactive items never have a row at all, so
  // the real, user-visible proof this worked is the row disappearing.
  await expect(row).toHaveCount(0);

  assertClean();
});
