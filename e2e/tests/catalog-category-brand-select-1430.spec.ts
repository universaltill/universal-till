import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1430: the catalog item-edit form's category and brand fields were
// free-text inputs backed by a <datalist> whose OPTION VALUE was the raw
// lookup id — so both the create/edit form and the items table showed a
// GUID instead of a name. Fixed by replacing the inputs with <select>s
// (id submitted, name shown/selected) and adding a category name column to
// the items table.
//
// Drives the real demo-seeded catalog (001_init.sql) rather than importing
// fixture data -- "Food" and "Drinks" already exist as real root categories
// (see sale-screen-category-tabs-search-418.spec.ts), so this exercises the
// actual <select>'s options rather than a synthetic one this test injects.
test.describe('catalog category/brand select (ut-docs#1430)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('creating an item with a category shows the name, never the id, in the table and the edit form', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // The picker is a real <select>: the category id is never user-visible
    // anywhere in its markup once selected by label.
    const categorySelect = page.locator('#item-category');
    await expect(categorySelect).toHaveJSProperty('tagName', 'SELECT');
    await categorySelect.selectOption({ label: 'Food' });

    const name = 'Category Select Probe ' + Date.now();
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('3.50');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    // The new row's own category cell shows "Food" by name (AC: items
    // table shows the category name column).
    const row = page.locator('.catalog-row', { hasText: name });
    await expect(row).toBeVisible();
    await expect(row).toContainText('Food');
    // Never the raw id anywhere in the row's rendered text.
    const categoryId = await categorySelect.locator('option', { hasText: 'Food' }).getAttribute('value');
    expect(categoryId).toBeTruthy();
    await expect(row).not.toContainText(categoryId as string);

    // Loading it back into the edit form selects "Food" by id -- the
    // select's chosen option's visible text is the name, its value is the
    // id (AC's own e2e wording).
    await row.locator('td').first().click();
    await expect(page.locator('#item-id')).not.toHaveValue('');
    await expect(categorySelect).toHaveValue(categoryId as string);
    const selectedText = await categorySelect.evaluate(
      (el: HTMLSelectElement) => el.options[el.selectedIndex].textContent,
    );
    expect(selectedText?.trim()).toBe('Food');

    assertClean();
  });

  test('an item with no category shows the empty placeholder, not a blank id', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const name = 'No Category Probe ' + Date.now();
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    // Leave #item-category on its default "none" option.
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    const row = page.locator('.catalog-row', { hasText: name });
    await expect(row).toBeVisible();
    await expect(row.locator('td').nth(6)).toHaveText('—');

    assertClean();
  });

  // The barcode-autofill JS used to match a looked-up product's brand
  // against the now-deleted #brands-list datalist's own <option>s; ut-docs#1430
  // retargeted it to the #item-brand <select>'s own options (same id/name
  // data, no separate datalist to keep in sync). Found untested while
  // Testing this card -- no existing spec drove the autofill path at all.
  test('barcode autofill selects the matching brand by name in the retargeted select', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    await page.route('**/api/catalog/lookup**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            name: 'Autofill Brand Probe ' + Date.now(),
            description: 'Cola',
            brand: 'Coca-Cola',
            quantity: '330ml',
            image_url: '',
            source: 'test-fixture',
          },
          error: null,
        }),
      }),
    );

    await page.locator('#item-barcode').fill('5000112548167');
    await page.locator('#autofill-btn').click();
    await expect(page.locator('#autofill-msg')).toContainText('Found');

    const brandSelect = page.locator('#item-brand');
    await expect(brandSelect).toHaveJSProperty('tagName', 'SELECT');
    const selectedText = await brandSelect.evaluate(
      (el: HTMLSelectElement) => el.options[el.selectedIndex]?.textContent,
    );
    expect(selectedText?.trim()).toBe('Coca-Cola');

    assertClean();
  });
});
