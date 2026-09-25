import { test, expect } from './fixtures';

// ut-docs#2330: the item-editor's modifiers surface (a nested dialog then,
// the Modifiers tab since ut-docs#2211) is attach/detach-only — no inline group/option create/edit/delete reachable
// from there (that stays exclusively on /modifiers). This locks in the
// actual browser-driven behaviour the Go handler tests
// (modifiers_admin_test.go, category_inherit_2284_test.go) can't see:
// real htmx round-trips, the checkbox-list attach control genuinely
// attaching what was checked, and the confirm() on detach.

const RUN = Date.now().toString(36).toUpperCase();

async function createItem(page: import('@playwright/test').Page, name: string): Promise<string> {
  const resp = await page.request.post('/api/catalog/item', { form: { name, price: '150' } });
  expect(resp.ok(), `create item ${name}`).toBe(true);
  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  await expect(row).toHaveCount(1);
  return (await row.first().getAttribute('data-id'))!;
}

test.describe('ut-docs#2330 item-editor Manage Modifiers is attach/detach-only', () => {
  let ownerItemId: string | undefined;
  let targetItemId: string | undefined;
  let groupId: string | undefined;

  test.afterEach(async ({ page }) => {
    for (const id of [ownerItemId, targetItemId]) {
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } }).catch(() => {});
    }
    ownerItemId = targetItemId = groupId = undefined;
  });

  test('attach-only surface has no CRUD, attaches via checkbox list, and detach reverses it', async ({ page }) => {
    const ownerName = `Attach Owner ${RUN}`;
    const targetName = `Attach Target ${RUN}`;
    const groupName = `Milk ${RUN}`;

    // The group starts life owned by a throwaway "owner" item — this is
    // exactly the shape /modifiers' shop-wide CRUD exists for — so it
    // shows up as ATTACHABLE (not yet linked) on the item this test
    // actually drives the dialog against.
    ownerItemId = await createItem(page, ownerName);
    const groupResp = await page.request.post('/api/catalog/modifier-group', {
      form: { itemId: ownerItemId, name: groupName, minSelect: '0', maxSelect: '1' },
    });
    expect(groupResp.ok(), 'create modifier group').toBe(true);

    targetItemId = await createItem(page, targetName);

    // data-group-id (ut-docs#2330) is the stable way to read a group's id
    // back off either render mode — see this same card's fix to
    // sell-screen-categories-tab-2283.spec.ts's seedModifier for why the
    // old name="id" input scan no longer works against this panel.
    const panelResp = await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${ownerItemId}`);
    const panelHtml = await panelResp.text();
    const nameIdx = panelHtml.indexOf(`>${groupName}<`);
    expect(nameIdx, 'owner panel must show the new group').toBeGreaterThan(-1);
    const idMatch = [...panelHtml.slice(0, nameIdx).matchAll(/data-group-id="([^"]*)"/g)];
    expect(idMatch.length).toBeGreaterThan(0);
    groupId = idMatch[idMatch.length - 1][1];

    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${targetItemId}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    // ut-docs#2211: the picker is inline in the item editor's own
    // Modifiers tab now, not a nested dialog opened from Variants.
    await page.locator('#item-form-tab-modifiers').click();
    await expect(page.locator('#item-form-panel-modifiers')).toBeVisible();

    const dialogList = page.locator('#item-form-panel-modifiers #item-modifiers-list');

    // No CRUD reachable from here: no "add a new group" form, no editable
    // group-name input anywhere in this tab.
    await expect(dialogList.locator('.modifier-admin-group-new')).toHaveCount(0);
    await expect(dialogList.locator('input[name="name"]')).toHaveCount(0);

    // The group is offered as a checkbox, not a <select> option.
    const checkbox = dialogList.locator(`input[type="checkbox"][name="groupId"][value="${groupId}"]`);
    await expect(checkbox).toHaveCount(1);
    await expect(dialogList.locator('.modifier-admin-attach-multi select')).toHaveCount(0);

    await checkbox.check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-group/attach') && r.request().method() === 'POST'),
      dialogList.locator('.modifier-admin-attach-multi button[type="submit"]').click(),
    ]);

    // Attached: now a read-only row (name text, no input), with a Detach
    // button — and no longer offered as a checkbox to attach again.
    const attachedRow = dialogList.locator(`.modifier-admin-group[data-group-id="${groupId}"]`);
    await expect(attachedRow).toHaveCount(1);
    await expect(attachedRow.locator('.modifier-admin-group-name')).toHaveText(groupName);
    await expect(attachedRow.locator('input[name="name"]')).toHaveCount(0);
    await expect(dialogList.locator(`input[type="checkbox"][name="groupId"][value="${groupId}"]`)).toHaveCount(0);

    // Detach reverses it — accept the native confirm() (hx-confirm).
    page.once('dialog', (d) => d.accept());
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-group/detach') && r.request().method() === 'POST'),
      attachedRow.locator('button', { hasText: 'Remove from this item' }).click(),
    ]);

    await expect(dialogList.locator(`.modifier-admin-group[data-group-id="${groupId}"]`)).toHaveCount(0);
    await expect(dialogList.locator(`input[type="checkbox"][name="groupId"][value="${groupId}"]`)).toHaveCount(1);
  });
});
