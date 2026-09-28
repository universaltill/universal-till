import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { ensureOperator, ADMIN_PIN, setBrowsingMode } from './helpers';

// ut-docs#2534 (owner rule): editing from the all_filter_chips All grid is
// for an admin, a manager, or a role holding catalog_management only. A
// cashier's session renders the grid WITHOUT data-edit-allowed, so a long
// press or right-click there never arms the jiggle mode (the tap still
// sells, as always), and a hand-made subset reorder POST lands on the
// manager-PIN elevation prompt and is not persisted.
//
// Needs the `auth` project (a real cashier session) -- listed in
// playwright.config.ts's AUTH_ONLY_SPECS. The auth till is shared by that
// project's files in file-sort order, so this spec creates its own cashier
// and puts the browsing mode back (category_tabs, the till's default) at
// the end.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier2534${RUN}`;
const CASHIER_PIN = '246813';
const CAT = `AllJigCash2534 ${RUN}`;
const ITEMS = ['Alpha', 'Bravo'].map((n) => ({ name: `AllJigCash2534 ${n} ${RUN}`, sku: `AJC2534${n}${RUN}` }));

async function seed(page: Page): Promise<string> {
  const csv = 'Name,SKU,Barcode,Price,Category,In stock\n' + ITEMS.map((i) => `${i.name},${i.sku},,1.00,${CAT},1`).join('\n');
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-2534c-${RUN}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/categories');
  const row = page.locator(`.category-row[data-field-name="${CAT}"]`);
  await expect(row).toHaveCount(1);
  return (await row.getAttribute('data-id'))!;
}

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 2534', role: 'cashier' },
  });
  expect(createResp.ok(), 'create cashier user').toBe(true);
  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];
  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginWithPin(page: Page, pin: string): Promise<void> {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of pin.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
}

async function longPress(tile: Locator) {
  await tile.scrollIntoViewIfNeeded();
  const page = tile.page();
  const box = (await tile.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(700);
  await page.mouse.up();
}

const namesIn = (page: Page) =>
  page.locator('#buttons-grid-all .btn-tile[data-name^="AllJigCash2534 "]').evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.name));

test.describe('All grid jiggle edit mode is not for a cashier (ut-docs#2534)', () => {
  test('a cashier long press never arms edit mode (it sells like a tap), and a subset reorder is refused and not persisted', async ({ page }) => {
    await ensureOperator(page); // admin
    await setBrowsingMode(page, 'all_filter_chips');
    const catId = await seed(page);
    await createCashier(page);
    await loginWithPin(page, CASHIER_PIN);
    try {
      await page.goto('/');
      const grid = page.locator('#buttons-grid-all');
      await expect(grid).toBeVisible();
      await expect(grid).not.toHaveAttribute('data-edit-allowed', /.*/);
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes(`category=${catId}`)),
        page.locator(`#browsing-category-chips .chip[data-cat-id="${catId}"]`).click(),
      ]);
      await expect.poll(() => namesIn(page)).toEqual(ITEMS.map((i) => i.name));
      const alpha = grid.locator(`.btn-tile[data-name="${ITEMS[0].name}"]`);

      // Right-click: no edit mode, no badges.
      await alpha.click({ button: 'right' });
      await page.waitForTimeout(300);
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);
      await expect(page.locator('[data-testid="jiggle-bar"]')).toBeHidden();
      await expect(grid.locator('[data-testid="tile-badge-edit"]')).toHaveCount(0);

      // Long press: no edit mode either -- it ends as the ordinary tap it
      // always was, so the item is rung up.
      await longPress(alpha);
      await page.waitForTimeout(300);
      await expect(page.locator('#buttons-grid')).not.toHaveClass(/jiggle-mode/);
      await expect(grid.locator('[data-testid="tile-badge-edit"]')).toHaveCount(0);
      await expect(page.locator('.basket .line-name', { hasText: ITEMS[0].name })).toHaveCount(1);
      await page.request.post('/api/pos/reset');

      // The server refuses a hand-made subset reorder: elevation prompt,
      // nothing saved.
      const codes = await grid.locator('.btn-tile[data-name^="AllJigCash2534 "]').evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code!));
      const form = new URLSearchParams();
      form.append('scope', 'subset');
      for (const c of [...codes].reverse()) form.append('codes', c);
      const resp = await page.request.post('/api/buttons/reorder', {
        data: form.toString(),
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      });
      expect(resp.headers()['x-ut-response']).toBe('elevation-prompt');
      await page.reload();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/ui/buttons/all/more') && r.url().includes(`category=${catId}`)),
        page.locator(`#browsing-category-chips .chip[data-cat-id="${catId}"]`).click(),
      ]);
      await expect.poll(() => namesIn(page)).toEqual(ITEMS.map((i) => i.name));
    } finally {
      await page.request.post('/api/pos/reset');
      await loginWithPin(page, ADMIN_PIN); // back to the admin for cleanup
      await setBrowsingMode(page, 'category_tabs');
      await page.goto('/catalog');
      for (const it of ITEMS) {
        const row = page.locator(`.catalog-row[data-name="${it.name}"]`);
        if ((await row.count()) === 0) continue;
        const id = await row.first().getAttribute('data-id');
        if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
      }
    }
  });
});
