import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

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
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('2.00');
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

  const row = page.locator('.catalog-row', { hasText: name });
  await expect(row).toBeVisible();

  // Load it into the form (a plain cell, not a .btn, per the row-click
  // handler's own exclusion) — the Active box starts checked, since only
  // active items ever have a row to click.
  await row.locator('td').first().click();
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
