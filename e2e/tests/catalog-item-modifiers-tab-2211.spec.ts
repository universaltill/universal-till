import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2211 (reopened): the item editor has a top-level Modifiers tab
// right after Variants. Everything modifier-related — the item's own
// groups, the attach/detach picker (#2330), the category-inherited groups
// (#2284) and the way out to /modifiers (#2379) — renders inline in that
// tab; the Variants tab carries none of it. The first close-out of this
// card never added the tab, so this drives the real browser path end to
// end: tab order, attach, reopen, keyboard arrows, RTL.

const RUN = Date.now().toString(36).toUpperCase();

async function createItem(page: import('@playwright/test').Page, name: string): Promise<string> {
  const resp = await page.request.post('/api/catalog/item', { form: { name, price: '150' } });
  expect(resp.ok(), `create item ${name}`).toBe(true);
  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  await expect(row).toHaveCount(1);
  return (await row.first().getAttribute('data-id'))!;
}

async function openItem(page: import('@playwright/test').Page, id: string) {
  await page.locator(`.catalog-row[data-id="${id}"]`).click();
  await expect(page.locator('#item-form-modal')).toBeVisible();
}

const TAB_ORDER = [
  'item-form-tab-details',
  'item-form-tab-variants',
  'item-form-tab-modifiers',
  'item-form-tab-image',
  'item-form-tab-labels',
  'item-form-tab-keypad',
];

test.describe('ut-docs#2211 item editor Modifiers tab', () => {
  const cleanup: string[] = [];

  test.afterEach(async ({ page }) => {
    for (const id of cleanup.splice(0)) {
      await page.request.post('/api/catalog/item/deactivate', { form: { id } }).catch(() => {});
    }
  });

  test('Modifiers sits after Variants; attach a group, save, reopen shows it; Variants has no modifier UI', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    const groupName = `Syrup ${RUN}`;
    // The group is born on a throwaway owner item (what /modifiers' shop-wide
    // CRUD does), so it is ATTACHABLE on the item this test drives.
    const ownerId = await createItem(page, `Mod Owner ${RUN}`);
    cleanup.push(ownerId);
    const g = await page.request.post('/api/catalog/modifier-group', {
      form: { itemId: ownerId, name: groupName, minSelect: '0', maxSelect: '1' },
    });
    expect(g.ok(), 'create modifier group').toBe(true);
    const targetId = await createItem(page, `Mod Target ${RUN}`);
    cleanup.push(targetId);

    await openItem(page, targetId);

    // Tab order, as the operator sees it.
    const ids = await page.locator('#item-form-modal [role="tab"]').evaluateAll((els) => els.map((e) => e.id));
    expect(ids).toEqual(TAB_ORDER);
    await expect(page.locator('#item-form-tab-modifiers')).toHaveText('Modifiers');

    // Variants tab: nothing modifier-related left in it.
    await page.locator('#item-form-tab-variants').click();
    const variants = page.locator('#item-form-panel-variants');
    await expect(variants).toBeVisible();
    await expect(variants.locator('#catalog-variants')).toBeVisible();
    await expect(variants).not.toContainText('Modifiers');
    await expect(variants.locator('[id*="modifier"], [class*="modifier"], [hx-get*="modifier"], [hx-post*="modifier"]')).toHaveCount(0);
    await expect(page.locator('#modifier-groups-modal')).toHaveCount(0);

    // Modifiers tab: the picker renders inline, no nested dialog.
    await page.locator('#item-form-tab-modifiers').click();
    await expect(page.locator('#item-form-tab-modifiers')).toHaveAttribute('aria-selected', 'true');
    const panel = page.locator('#item-form-panel-modifiers');
    await expect(panel).toBeVisible();
    await expect(variants).toBeHidden();
    const list = panel.locator('#item-modifiers-list');
    const option = list.locator('.modifier-attach-option', { hasText: groupName });
    const box = option.locator('input[type="checkbox"]');
    await expect(box).toBeVisible();
    // Box and group name on one row (the item form's column-label rule
    // must not reach this picker), and a full 44px tap target.
    expect(await option.evaluate((el) => getComputedStyle(el).flexDirection)).toBe('row');
    expect((await option.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await box.check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/modifier-group/attach') && r.request().method() === 'POST'),
      list.locator('.modifier-admin-attach-multi button[type="submit"]').click(),
    ]);
    await expect(list.locator('.modifier-admin-group-name', { hasText: groupName })).toBeVisible();

    // Save the item, close, reopen: the attachment persisted and shows on
    // the Modifiers tab of a fresh open.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await page.locator('#item-form-close-btn').click();
    await expect(page.locator('#item-form-modal')).toBeHidden();

    await page.goto('/catalog');
    await openItem(page, targetId);
    // Every open lands on Details, never on the last session's tab.
    await expect(page.locator('#item-form-tab-details')).toHaveAttribute('aria-selected', 'true');
    await page.locator('#item-form-tab-modifiers').click();
    await expect(page.locator('#item-modifiers-list .modifier-admin-group-name', { hasText: groupName })).toBeVisible();
    // ...and it is attached (a Remove button), not offered again as a checkbox.
    await expect(page.locator('#item-modifiers-list .modifier-attach-option', { hasText: groupName })).toHaveCount(0);

    // Screenshot for the close-out (ut-docs#2211 AC).
    const shotDir = process.env.UT_2211_SHOT_DIR;
    if (shotDir) await page.screenshot({ path: `${shotDir}/2211-modifiers-tab-1280x800.png` });
    assertClean();
  });

  test('keyboard: arrows walk Variants → Modifiers → Item image and back', async ({ page }) => {
    const assertClean = watchConsole(page);
    const id = await createItem(page, `Mod Keys ${RUN}`);
    cleanup.push(id);
    await openItem(page, id);

    await page.locator('#item-form-tab-variants').click();
    await page.locator('#item-form-tab-variants').focus();
    await page.keyboard.press('ArrowRight');
    await expect(page.locator('#item-form-tab-modifiers')).toBeFocused();
    await expect(page.locator('#item-form-tab-modifiers')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#item-form-tab-modifiers')).toHaveAttribute('tabindex', '0');
    await expect(page.locator('#item-form-panel-modifiers')).toBeVisible();
    await page.keyboard.press('ArrowRight');
    await expect(page.locator('#item-form-tab-image')).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(page.locator('#item-form-tab-modifiers')).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(page.locator('#item-form-tab-variants')).toBeFocused();
    assertClean();
  });

  test('RTL (fa): the tab is translated and ArrowLeft moves forward to it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const id = await createItem(page, `Mod RTL ${RUN}`);
    cleanup.push(id);
    await page.goto('/catalog?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await openItem(page, id);

    const tab = page.locator('#item-form-tab-modifiers');
    await expect(tab).not.toHaveText('Modifiers');
    const ids = await page.locator('#item-form-modal [role="tab"]').evaluateAll((els) => els.map((e) => e.id));
    expect(ids).toEqual(TAB_ORDER);
    // Reading order is mirrored: Modifiers sits to the LEFT of Variants.
    const vBox = (await page.locator('#item-form-tab-variants').boundingBox())!;
    const mBox = (await tab.boundingBox())!;
    expect(mBox.x, 'in RTL Modifiers renders to the left of Variants').toBeLessThan(vBox.x);

    await page.locator('#item-form-tab-variants').click();
    await page.locator('#item-form-tab-variants').focus();
    await page.keyboard.press('ArrowLeft');
    await expect(tab).toBeFocused();
    await expect(page.locator('#item-form-panel-modifiers')).toBeVisible();
    await page.goto('/catalog?lang=en');
    assertClean();
  });

  test('/modifiers item link opens the editor on the Modifiers tab', async ({ page }) => {
    const assertClean = watchConsole(page);
    const id = await createItem(page, `Mod Deep ${RUN}`);
    cleanup.push(id);
    await page.goto(`/catalog?item=${id}&tab=modifiers`);
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await expect(page.locator('#item-form-tab-modifiers')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#item-form-panel-modifiers #item-modifiers-list .catalog-detail-modifiers').first()).toBeVisible();
    assertClean();
  });

  test('kiosk floor 1024x600: the Modifiers tab and its attach button are reachable', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    const ownerId = await createItem(page, `Mod Floor Owner ${RUN}`);
    cleanup.push(ownerId);
    // Enough groups that the inline checkbox list is taller than the body.
    for (let i = 0; i < 10; i++) {
      const g = await page.request.post('/api/catalog/modifier-group', {
        form: { itemId: ownerId, name: `Floor Group ${i} ${RUN}`, minSelect: '0', maxSelect: '1' },
      });
      expect(g.ok()).toBe(true);
    }
    const id = await createItem(page, `Mod Floor ${RUN}`);
    cleanup.push(id);
    await openItem(page, id);
    const tab = page.locator('#item-form-tab-modifiers');
    await expect(tab).toBeInViewport({ ratio: 1 });
    await tab.click();
    const body = page.locator('.catalog-form-body');
    const submit = page.locator('#item-modifiers-list .modifier-admin-attach-multi button[type="submit"]');
    await expect(submit).toHaveCount(1);
    const scrollable = await body.evaluate((el) => el.scrollHeight > el.clientHeight);
    expect(scrollable, 'the Modifiers panel must be taller than .catalog-form-body at 1024x600 for this test to mean anything').toBe(true);
    await body.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await expect(submit).toBeInViewport({ ratio: 1 });
    const hit = await submit.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'the attach button must be the real hit-test target').toBe(true);
    await body.evaluate((el) => { el.scrollTop = 0; });
    const shotDir = process.env.UT_2211_SHOT_DIR;
    if (shotDir) await page.screenshot({ path: `${shotDir}/2211-modifiers-tab-1024x600.png` });
    assertClean();
  });
});
