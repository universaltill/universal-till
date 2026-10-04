import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#3606: one item row, three places holding its updated_at — the
// catalogue card (data-updated-at), the item editor's base_updated_at and
// the Variants tab's cost / lead time / reorder level forms. On an
// additional till each save sends its stamp to the main till as the
// conflict base, so after a save from one place the others must follow,
// or the operator's next save in the same open dialog reads as a conflict
// with their own previous save. Driven in the real dialog (catalog.html's
// followItemStamp); the stamps are read from the DOM the server rendered.

test.describe('item stamp follows a save across the editor and its panel (ut-docs#3606)', () => {
  test('a cost save advances the editor base and the card; an editor save advances the panel forms', async ({ page }) => {
    const assertClean = watchConsole(page);
    const name = `Stamp 3606 ${Date.now()}`;
    await page.goto('/catalog');
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('4.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await closeItemForm(page);

    const row = page.locator('.catalog-row', { hasText: name });
    await row.click();
    const base = page.locator('#item-base-updated-at');
    const s0 = await base.inputValue();
    expect(s0).not.toBe('');
    await page.locator('#item-form-tab-variants').click();
    const panel = page.locator('#catalog-variants');
    await expect(panel).toHaveAttribute('data-item-updated-at', s0);
    const costStamp = page.locator('form[hx-post="/api/catalog/item-cost"] input[name="base_updated_at"]');
    await expect(costStamp).toHaveValue(s0);

    // updated_at has one-second resolution: make sure the save lands in a later second.
    await page.waitForTimeout(1100);
    await page.locator('form[hx-post="/api/catalog/item-cost"] input[name="cost"]').fill('1.25');
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item-cost')),
      page.locator('form[hx-post="/api/catalog/item-cost"] button[type=submit]').click(),
    ]);
    expect(resp.status()).toBe(200);
    expect(resp.request().postData()).toContain(`base_updated_at=${encodeURIComponent(s0)}`);
    await expect(panel).not.toHaveAttribute('data-item-updated-at', s0);
    const s1 = (await panel.getAttribute('data-item-updated-at'))!;
    expect(s1 > s0).toBe(true);
    await expect(base).toHaveValue(s1);
    await expect(row).toHaveAttribute('data-updated-at', s1);

    // The other direction: an item details save advances the panel forms.
    await page.waitForTimeout(1100);
    await page.locator('#item-form-tab-details').click();
    await page.locator('#item-price').fill('4.50');
    const [resp2] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
      page.locator('#item-form-submit').click(),
    ]);
    expect(resp2.status()).toBe(200);
    expect(resp2.request().postData()).toContain(`base_updated_at=${encodeURIComponent(s1)}`);
    await expect(base).not.toHaveValue(s1);
    const s2 = await base.inputValue();
    expect(s2 > s1).toBe(true);
    await expect(costStamp).toHaveValue(s2);
    assertClean();
  });
});
