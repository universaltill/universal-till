import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2406 (follow-up from the ADR-0101/#2399 review's finding L5): a
// shop that cleans up many single-use-group items ends up with a pile of
// unassigned cards on /modifiers and only the per-group Delete to clear
// them one at a time. This drives the real page (manager, UT_AUTH=off) as
// a real operator would: create two unassigned groups plus one assigned
// group, filter to "only unassigned", then delete all unassigned in one
// confirmed action and prove the assigned group survives untouched.
test.describe('bulk-delete unassigned modifier groups (ut-docs#2406)', () => {
  test('filter to unassigned, delete all unassigned, assigned group survives', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });

    const stamp = Date.now();
    const categoryName = 'Bulk Del Cat ' + stamp;
    const catRes = await request.post('/api/categories', { form: { name: categoryName } });
    expect(catRes.ok(), 'POST /api/categories must succeed').toBe(true);

    await page.goto('/modifiers');
    const createForm = page.locator('.modifiers-new-group-card form');
    const cardFor = (name: string) => page.locator(`.modifier-card:has(input[name="name"][value="${name}"])`);

    const createGroup = async (name: string) => {
      await createForm.locator('input[name="name"]').fill(name);
      await Promise.all([
        page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group') && r.request().method() === 'POST'),
        createForm.locator('button[type="submit"]').click(),
      ]);
      await expect(cardFor(name)).toBeVisible();
    };

    const orphan1 = 'Bulk Orphan One ' + stamp;
    const orphan2 = 'Bulk Orphan Two ' + stamp;
    const kept = 'Bulk Kept ' + stamp;
    await createGroup(orphan1);
    await createGroup(orphan2);
    await createGroup(kept);

    // Assign "kept" to the category so it must survive the bulk delete.
    const keptCatBox = cardFor(kept)
      .locator('label.modifier-assign-cat', { hasText: categoryName })
      .locator('input[type="checkbox"]');
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group/attach-category') && r.request().method() === 'POST'),
      keptCatBox.check(),
    ]);
    await expect(cardFor(kept)).not.toContainText('Not assigned to any category or item yet');

    // The toolbar reports (at least) our two orphans and lets us filter
    // down to just the unassigned cards.
    const toolbar = page.locator('.modifiers-unassigned-toolbar');
    await expect(toolbar).toBeVisible();
    await toolbar.locator('#modifiers-filter-unassigned').check();
    await expect(cardFor(orphan1)).toBeVisible();
    await expect(cardFor(orphan2)).toBeVisible();
    await expect(cardFor(kept)).toBeHidden();
    await toolbar.locator('#modifiers-filter-unassigned').uncheck();
    await expect(cardFor(kept)).toBeVisible();

    // Delete all unassigned — confirm dialog accepted.
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group/delete-unassigned') && r.request().method() === 'POST'),
      toolbar.getByRole('button', { name: /delete all unassigned/i }).click(),
    ]);

    await expect(cardFor(orphan1)).toHaveCount(0);
    await expect(cardFor(orphan2)).toHaveCount(0);
    await expect(cardFor(kept)).toBeVisible();
    await expect(cardFor(kept)).not.toContainText('Not assigned to any category or item yet');
    // Nothing left unassigned: the toolbar itself is gone.
    await expect(page.locator('.modifiers-unassigned-toolbar')).toHaveCount(0);

    // Tidy: remove the surviving group everywhere.
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/catalog/modifier-group/delete') && r.request().method() === 'POST'),
      cardFor(kept).locator('.modifier-delete-btn').click(),
    ]);
    await expect(cardFor(kept)).toHaveCount(0);
    assertClean();
  });
});
