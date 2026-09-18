import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#2399 / ADR-0101: a modifier group is a SHOP-WIDE entity. It is
// created on /modifiers with no item at all, and assigned to categories
// and items by selection from its own card. Before this the /modifiers
// create form demanded an item (the product owner's "a modifier can only
// be created attached to a catalog item"), the page listed groups under
// each item, and a group with no item could not exist at the schema
// level.
//
// The spec drives the real page as a manager (the `default` project runs
// with UT_AUTH=off, so every route is already the operator's) and then
// proves the assignment took effect where it matters: the sale screen's
// customization picker (GET /ui/pos/modifiers, the same endpoint the
// product tile opens — pos_modifiers_api.go's ResolveGroupsForItem) offers
// the group for an item in the assigned category, and does NOT before the
// category is ticked.
test.describe('modifier groups are shop-wide (ut-docs#2399)', () => {
  test('create with no item, add an option, assign to a category, and the sale picker offers it', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });

    // A category of our own, so nothing else in the demo catalogue
    // interferes — through the real /api/categories form endpoint (the
    // /categories dialog's own action; 303 back to /categories on success).
    const stamp = Date.now();
    const categoryName = 'Modifier Cat ' + stamp;
    const catRes = await request.post('/api/categories', { form: { name: categoryName } });
    expect(catRes.ok(), 'POST /api/categories must succeed').toBe(true);

    // An item in that category, through the real catalog form. The SKU is
    // what the sale screen scans (Engine.ResolveBase), so it doubles as
    // the picker's ?code= below.
    const itemName = 'Modifier Probe ' + stamp;
    const sku = 'MODPROBE' + stamp;
    await page.goto('/catalog');
    await openNewItemForm(page);
    await page.locator('#item-name').fill(itemName);
    await page.locator('#item-price').fill('2.50');
    await page.locator('#item-sku').fill(sku);
    await page.locator('#item-category').selectOption({ label: categoryName });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    const itemRow = page.locator(`.catalog-row[data-sku="${sku}"]`);
    await expect(itemRow).toHaveCount(1);
    const itemId = await itemRow.getAttribute('data-id');
    expect(itemId, 'the probe item must carry its id on its catalog row').toBeTruthy();

    // /modifiers: the create form has NO item search input at all.
    await page.goto('/modifiers');
    const createForm = page.locator('.modifiers-new-group-card form');
    await expect(createForm).toBeVisible();
    await expect(createForm.locator('input[list="modifiers-items-list"]')).toHaveCount(0);
    await expect(createForm.locator('input[name="itemId"]')).toHaveCount(0);

    // Create a group with just a name and rules.
    const groupName = 'Sauces ' + stamp;
    await createForm.locator('input[name="name"]').fill(groupName);
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group') && r.request().method() === 'POST'),
      createForm.locator('button[type="submit"]').click(),
    ]);
    // The group's name is the card's <input value="…">, not text content,
    // so locate the card by that input rather than by hasText.
    const cardFor = (name: string) => page.locator(`.modifier-card:has(input[name="name"][value="${name}"])`);
    const card = cardFor(groupName);
    await expect(card).toBeVisible();
    await expect(card, 'a brand-new group is listed as unassigned').toContainText('Not assigned to any category or item yet');

    // Add an option from the card.
    const addOption = card.locator('form.modifier-admin-option-row').last();
    await addOption.locator('input[name="name"]').fill('Ketchup');
    await addOption.locator('input[name="priceDeltaMajor"]').fill('0.20');
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-option') && r.request().method() === 'POST'),
      addOption.locator('button[type="submit"]').click(),
    ]);
    await expect(cardFor(groupName).locator('input[name="name"][value="Ketchup"]')).toBeVisible();

    // Not offered at checkout yet: nothing links the group to anything.
    const before = await request.get(`/ui/pos/modifiers?item=${encodeURIComponent(itemId)}&code=${encodeURIComponent(sku)}`);
    expect(before.status()).toBe(200);
    expect(await before.text(), 'an unassigned group must not be offered at checkout').not.toContain(groupName);

    // Assign to the category by ticking its checkbox on the card.
    const catBox = cardFor(groupName)
      .locator('label.modifier-assign-cat', { hasText: categoryName })
      .locator('input[type="checkbox"]');
    await expect(catBox).not.toBeChecked();
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group/attach-category') && r.request().method() === 'POST'),
      catBox.check(),
    ]);
    await expect(cardFor(groupName)
      .locator('label.modifier-assign-cat', { hasText: categoryName })
      .locator('input[type="checkbox"]')).toBeChecked();
    await expect(cardFor(groupName)).not.toContainText('Not assigned to any category or item yet');

    // The sale-screen picker now offers the group for an item in that
    // category — resolved through the category link, no item link at all.
    const after = await request.get(`/ui/pos/modifiers?item=${encodeURIComponent(itemId)}&code=${encodeURIComponent(sku)}`);
    expect(after.status()).toBe(200);
    const pickerHtml = await after.text();
    expect(pickerHtml).toContain(groupName);
    expect(pickerHtml).toContain('Ketchup');

    // Tidy: delete the group everywhere from its card (confirm dialog
    // accepted), and the picker no longer offers it.
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group/delete') && r.request().method() === 'POST'),
      cardFor(groupName).locator('.modifier-delete-btn').click(),
    ]);
    await expect(cardFor(groupName)).toHaveCount(0);
    const gone = await request.get(`/ui/pos/modifiers?item=${encodeURIComponent(itemId)}&code=${encodeURIComponent(sku)}`);
    expect(await gone.text()).not.toContain(groupName);
    assertClean();
  });

  test('a fresh /modifiers renders the create form without any item search input', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/modifiers');
    const createForm = page.locator('.modifiers-new-group-card form');
    await expect(createForm).toBeVisible();
    await expect(createForm.locator('input[name="name"]')).toBeVisible();
    await expect(createForm.locator('input[list="modifiers-items-list"]')).toHaveCount(0);
    await expect(createForm.locator('input[name="itemId"]')).toHaveCount(0);
    // No horizontal overflow at the kiosk floor.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow, 'no horizontal page scroll at 1024x600').toBe(false);
    assertClean();
  });
});
