import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2727: the Modifiers tab's Remove / Attach / Skip / Use again
// outerHTML-swap #item-modifiers-list. Before, the buttons had no stable
// id, so htmx could not restore focus and a keyboard / switch user landed
// on <body>. Keyboard-only: focus() + Enter/Space, then assert where
// document.activeElement ends up.

const RUN = Date.now().toString(36).toUpperCase();
const PANEL = '#item-form-panel-modifiers';

async function activeId(page: Page): Promise<string> {
  return page.evaluate(() => (document.activeElement as HTMLElement | null)?.id ?? '');
}
async function activeInPanel(page: Page): Promise<boolean> {
  return page.evaluate((sel) => {
    const a = document.activeElement;
    return !!a && a !== document.body && !!document.querySelector(sel)?.contains(a);
  }, PANEL);
}

async function mkGroup(page: Page, ownerId: string, name: string): Promise<string> {
  const r = await page.request.post('/api/catalog/modifier-group', {
    form: { itemId: ownerId, name, minSelect: '0', maxSelect: '1' },
  });
  expect(r.ok(), `create group ${name}`).toBe(true);
  // The id is only visible on the owner's own panel.
  const html = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${ownerId}`)).text();
  const m = html.match(new RegExp(`data-group-id="([^"]+)"(?:(?!data-group-id)[\\s\\S])*?modifier-admin-group-name">${name}<`));
  expect(m, `group id for ${name}`).toBeTruthy();
  return m![1];
}

test.describe('ut-docs#2727 Modifiers tab keeps keyboard focus across swaps', () => {
  const items: string[] = [];
  const catLinks: Array<[string, string]> = [];

  test.afterEach(async ({ page }) => {
    for (const [g, c] of catLinks.splice(0)) {
      await page.request.post('/api/catalog/modifier-group/detach-category', { form: { groupId: g, categoryId: c } }).catch(() => {});
    }
    for (const id of items.splice(0)) {
      await page.request.post('/api/catalog/item/deactivate', { form: { id } }).catch(() => {});
    }
  });

  async function setup(page: Page) {
    await page.goto('/catalog');
    const catId = await page
      .locator('#item-form select[name="categoryId"] option:not([value=""])')
      .first()
      .getAttribute('value');
    expect(catId, 'demo catalog has a category').toBeTruthy();
    const mk = async (name: string, categoryId?: string) => {
      const r = await page.request.post('/api/catalog/item', { form: { name, price: '150', ...(categoryId ? { categoryId } : {}) } });
      expect(r.ok()).toBe(true);
      await page.goto('/catalog');
      const id = (await page.locator(`.catalog-row[data-name="${name}"]`).first().getAttribute('data-id'))!;
      items.push(id);
      return id;
    };
    const owner = await mk(`Focus Owner ${RUN}-${Math.random().toString(36).slice(2, 6)}`);
    const target = await mk(`Focus Target ${RUN}-${Math.random().toString(36).slice(2, 6)}`, catId!);
    const tag = Math.random().toString(36).slice(2, 6);
    const gInh = await mkGroup(page, owner, `Inh ${tag}`);
    const gA = await mkGroup(page, owner, `Alpha ${tag}`);
    const gB = await mkGroup(page, owner, `Beta ${tag}`);
    const l = await page.request.post('/api/catalog/modifier-group/attach-category', { form: { groupId: gInh, categoryId: catId! } });
    expect(l.ok()).toBe(true);
    catLinks.push([gInh, catId!]);
    await page.goto('/catalog');
    await page.locator(`.catalog-row[data-id="${target}"]`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    return { gInh, gA, gB };
  }

  test('open does not steal focus; attach / skip / use again / detach keep focus in the panel', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    const { gInh, gA, gB } = await setup(page);
    page.on('dialog', (d) => d.accept());

    // Open the tab by keyboard: focus stays on the tab, never the heading.
    await page.locator('#item-form-tab-modifiers').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`${PANEL} #item-modifiers-heading`)).toBeAttached();
    await expect(page.locator(`${PANEL} #modifier-attach-submit`)).toBeVisible();
    expect(await activeId(page)).toBe('item-form-tab-modifiers');

    // Attach one of two groups: more remain, so focus stays on the submit.
    await page.locator(`${PANEL} input[name="groupId"][value="${gA}"]`).focus();
    await page.keyboard.press('Space');
    await expect(page.locator(`${PANEL} input[name="groupId"][value="${gA}"]`)).toBeChecked();
    await page.locator('#modifier-attach-submit').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`${PANEL} #modifier-detach-${gA}`)).toBeVisible();
    await expect.poll(() => activeId(page)).toBe('modifier-attach-submit');

    // Skip the inherited group: the toggle keeps focus, and so does Use again.
    const toggle = `modifier-inherited-toggle-${gInh}`;
    await page.locator(`#${toggle}`).focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`${PANEL} [data-inherited-group="${gInh}"].vg-inactive`)).toBeVisible();
    await expect.poll(() => activeId(page)).toBe(toggle);
    await page.keyboard.press('Space');
    await expect(page.locator(`${PANEL} [data-inherited-group="${gInh}"]:not(.vg-inactive)`)).toBeVisible();
    await expect.poll(() => activeId(page)).toBe(toggle);

    // Detach: the row (and its button) is removed, so the heading takes focus.
    await page.locator(`#modifier-detach-${gA}`).focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`${PANEL} #modifier-detach-${gA}`)).toHaveCount(0);
    await expect.poll(() => activeId(page)).toBe('item-modifiers-heading');
    expect(gB).toBeTruthy();

    if (process.env.UT_2727_SHOT_DIR) await page.screenshot({ path: `${process.env.UT_2727_SHOT_DIR}/2727-after-detach-1024x600.png` });
    assertClean();
  });

  test('attach-all then detach lands on the heading when the focused control is gone', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 700 });
    const { gA, gB } = await setup(page);
    page.on('dialog', (d) => d.accept());
    await page.locator('#item-form-tab-modifiers').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('#modifier-attach-submit')).toBeVisible();

    // Check every attachable group by keyboard and attach them in one go.
    const boxes = page.locator(`${PANEL} input[name="groupId"]`);
    const n = await boxes.count();
    for (let i = 0; i < n; i++) {
      await boxes.nth(i).focus();
      await page.keyboard.press('Space');
    }
    await page.locator('#modifier-attach-submit').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`#modifier-detach-${gB}`)).toBeVisible();
    // Attaching everything empties the attach list: the submit that had
    // focus is gone, so the heading takes it; otherwise it stays on submit.
    await expect.poll(() => activeId(page)).toBe((await page.locator('#modifier-attach-submit').count()) ? 'modifier-attach-submit' : 'item-modifiers-heading');

    await page.locator(`#modifier-detach-${gB}`).focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(`#modifier-detach-${gB}`)).toHaveCount(0);
    await expect.poll(() => activeId(page)).toBe('item-modifiers-heading');
    expect(gA).toBeTruthy();
    if (process.env.UT_2727_SHOT_DIR) await page.screenshot({ path: `${process.env.UT_2727_SHOT_DIR}/2727-after-detach-360.png` });
    assertClean();
  });
});
