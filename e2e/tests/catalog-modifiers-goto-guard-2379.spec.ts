import { test, expect } from './fixtures';

// ut-docs#2379: the item-editor's nested "Manage Modifiers" dialog gets a
// "Go to Modifiers" control when the shop has zero modifier groups
// anywhere (proven server-side, deterministically, by the Go handler tests
// — modifiers_zero_groups_2379_test.go — since this suite runs several
// workers against one shared DB and other specs' seeded groups would make
// a real "zero groups shop-wide" precondition flaky/order-dependent here).
//
// What this spec proves instead, which the Go tests can't see: the LIVE
// browser guard behind that control, catalog.html's
// window.utCatalogGoToModifiers — same-tab navigation only (never
// target="_blank"/window.open, the kiosk build is fully chromeless), and a
// native confirm() only when the item form actually has unsaved edits,
// never a silent loss. Called directly via page.evaluate rather than
// through the button's own click, so it exercises the real function
// regardless of whether this run's shared DB happens to have the button's
// own empty-state condition met.

const RUN = Date.now().toString(36).toUpperCase();

async function createItem(page: import('@playwright/test').Page, name: string): Promise<string> {
  const resp = await page.request.post('/api/catalog/item', { form: { name, price: '150' } });
  expect(resp.ok(), `create item ${name}`).toBe(true);
  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  await expect(row).toHaveCount(1);
  return (await row.first().getAttribute('data-id'))!;
}

test.describe('ut-docs#2379 go-to-Modifiers guard (catalog item editor)', () => {
  let itemId: string | undefined;

  test.afterEach(async ({ page }) => {
    if (itemId) await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } }).catch(() => {});
    itemId = undefined;
  });

  test('clean form navigates straight to /modifiers, no confirm prompt', async ({ page }) => {
    const name = `Goto Clean ${RUN}`;
    itemId = await createItem(page, name);

    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${itemId}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();

    let dialogFired = false;
    page.once('dialog', (d) => {
      dialogFired = true;
      d.dismiss();
    });

    await page.evaluate(() => (window as unknown as { utCatalogGoToModifiers: () => boolean }).utCatalogGoToModifiers());
    await page.waitForURL('**/modifiers');

    expect(dialogFired, 'a pristine, just-opened item form must never prompt').toBe(false);
    await expect(page).toHaveURL(/\/modifiers$/);
  });

  test('dirty form prompts, Cancel keeps the edits and stays put', async ({ page }) => {
    const name = `Goto Dirty Cancel ${RUN}`;
    itemId = await createItem(page, name);

    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${itemId}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();

    await page.locator('#item-name').fill(`${name} EDITED`);

    let promptMessage = '';
    page.once('dialog', (d) => {
      promptMessage = d.message();
      d.dismiss();
    });
    // window.confirm() is a blocking native call — Playwright's dialog
    // handler answers it via CDP before this evaluate()'s own promise can
    // resolve, and utCatalogGoToModifiers returns BEFORE calling
    // location.assign on the cancelled path (see catalog.html: `if
    // (!window.confirm(...)) return false;`). So by the time this await
    // returns, the guard has already made its decision either way — no
    // arbitrary settle-timeout needed to avoid asserting before the fact.
    await page.evaluate(() => (window as unknown as { utCatalogGoToModifiers: () => boolean }).utCatalogGoToModifiers());

    await expect(page).not.toHaveURL(/\/modifiers$/);
    expect(promptMessage.length, 'expected a real, non-empty discard-confirm message').toBeGreaterThan(0);
    await expect(page.locator('#item-name')).toHaveValue(`${name} EDITED`);
  });

  // ut-docs#2379 independent review: the FIRST implementation snapshotted
  // via FormData(form), and #item-price — the field the operator actually
  // types into — carries no `name` attribute at all (only the hidden
  // #item-price-minor, written once, at submit, does), so a price-only
  // edit was byte-identical to the snapshot and slipped through with NO
  // confirm — a silent loss the card's own acceptance criteria forbids.
  // Locks in the fix (formSession, bumped by a bubbling native 'input'
  // event regardless of the field's `name` attribute).
  test('editing ONLY the price still counts as dirty (regression, ut-docs#2379 review)', async ({ page }) => {
    const name = `Goto Price Only ${RUN}`;
    itemId = await createItem(page, name);

    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${itemId}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();

    await page.locator('#item-price').fill('9.99');

    let promptMessage = '';
    page.once('dialog', (d) => {
      promptMessage = d.message();
      d.dismiss();
    });
    await page.evaluate(() => (window as unknown as { utCatalogGoToModifiers: () => boolean }).utCatalogGoToModifiers());

    expect(promptMessage.length, 'a price-only edit must still trigger the discard-confirm').toBeGreaterThan(0);
    await expect(page).not.toHaveURL(/\/modifiers$/);
    await expect(page.locator('#item-price')).toHaveValue('9.99');
  });

  test('dirty form prompts, OK discards the edits and navigates', async ({ page }) => {
    const name = `Goto Dirty OK ${RUN}`;
    itemId = await createItem(page, name);

    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${itemId}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();

    await page.locator('#item-name').fill(`${name} EDITED`);

    page.once('dialog', (d) => d.accept());
    await page.evaluate(() => (window as unknown as { utCatalogGoToModifiers: () => boolean }).utCatalogGoToModifiers());
    await page.waitForURL('**/modifiers');

    await expect(page).toHaveURL(/\/modifiers$/);
  });
});
