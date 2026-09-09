import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#1365: the item-form submit button was never disabled while a
// save request was in flight — harmless before ut-docs#1363 (every
// response re-rendered the whole table), but #1363's row-level OOB
// protocol decides insert-vs-in-place from the item's PREVIOUS active
// state, read once before the mutation. Two rapid clicks while
// reactivating a formerly-inactive item (its edit form still populated
// after the row was deactivated out from under it — see handlers.go's
// own "Reachable for real" comment on the update handler) both read
// wasActive=false before either write lands, so both responses emit a
// row insert instead of the second one updating the row the first just
// created: two DOM elements sharing the same id, until the page reloads.
test.describe('catalog item-form double-submit (ut-docs#1365)', () => {
  test('rapid double-submit while reactivating a deactivated item does not duplicate its row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const name = 'Reactivate Double Submit ' + Date.now();
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    // ut-docs#1901: close explicitly — the dialog is non-modal (.show(),
    // ut-docs#1385's OSK fix), so nothing outside it is inert, but its
    // large `position: fixed` box covers and intercepts the row click
    // below.
    await page.locator('#item-form-close-btn').click();

    const row = page.locator('.catalog-row', { hasText: name });
    await expect(row).toBeVisible();

    // Load it into the edit form (opens the dialog).
    await row.locator('td').first().click();
    const itemId = await page.locator('#item-id').inputValue();
    expect(itemId).not.toBe('');
    await expect(page.locator('#item-active')).toBeChecked();

    // ut-docs#1901: deactivate it WITHOUT touching the open edit dialog —
    // reproduces the exact precondition handlers.go's update handler
    // calls out: "deactivate a row while the edit form still holds that
    // item, then save." Pre-#1901 this was a second UI element (the row's
    // own danger button) reachable on the same page alongside the
    // always-visible side panel. The editor is a <dialog> now — non-modal
    // (.show(), ut-docs#1385's OSK fix), so that button is NOT inert, but
    // the dialog's large `position: fixed` box (z-index 500, 94vw wide,
    // up to 92vh tall from 4vh down) sits over the list and intercepts
    // the click, and closing the dialog first would destroy the very
    // precondition under test (the form must still hold the item). So
    // this drives the same server-side race via a direct request instead,
    // which is arguably the MORE realistic reproduction anyway (a second
    // tab/operator deactivating the item while this one still has it open
    // for editing, not two controls on one screen that a real user could
    // never both reach at once).
    const deactivateResp = await page.request.post('/api/catalog/item/deactivate', {
      form: { id: itemId },
    });
    expect(deactivateResp.ok()).toBeTruthy();
    // This went via a raw request, not a page click, so htmx never
    // processed the response's row-removal OOB fragment — the stale
    // pre-deactivation row is still sitting in the DOM. Remove it
    // ourselves (same end state a real click's OOB delete would have
    // left), or the reactivation below inserts a SECOND row for the same
    // item alongside this stale one — a false failure of the very
    // "does not duplicate its row" assertion this test exists to make,
    // caused by this test's own request bypass, not a real product bug.
    await row.evaluate((el) => el.remove());

    // The form still holds the now-inactive item, Active still checked —
    // saving from here is a reactivation. Hold the update response so both
    // synchronous clicks below are dispatched before either request
    // resolves, and count how many actually reach the server: the fix
    // disables the button in the FIRST click's own (synchronous) handler,
    // so a disabled button's own click() never dispatches a second submit
    // — this must stay at 1 regardless of network timing.
    let updateRequests = 0;
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/catalog/item/update', async (route) => {
      updateRequests++;
      const response = await route.fetch();
      await held;
      await route.fulfill({ response });
    });

    await page.locator('#item-form-submit').evaluate((el: HTMLButtonElement) => {
      // Two synchronous DOM clicks in the same tick — the worst case for
      // the race: no `await` between them for the fix's disabled state to
      // lose a timing gamble against.
      el.click();
      el.click();
    });

    await expect.poll(() => updateRequests).toBe(1);
    await expect(page.locator('#item-form-submit')).toBeDisabled();
    release();

    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await expect(page.locator('#item-form-submit')).toBeEnabled();
    // The real bug symptom: exactly one row, not two sharing the same id.
    await expect(page.locator('.catalog-row', { hasText: name })).toHaveCount(1);

    assertClean();
  });

  test('the submit button is disabled for the duration of a save request', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/catalog/item', async (route) => {
      const response = await route.fetch();
      await held;
      await route.fulfill({ response });
    });

    const name = 'Submit Disabled Probe ' + Date.now();
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    await page.locator('#item-form-submit').click();

    await expect(page.locator('#item-form-submit')).toBeDisabled();
    release();

    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await expect(page.locator('#item-form-submit')).toBeEnabled();

    assertClean();
  });
});
