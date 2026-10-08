import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone } from './helpers';

// ut-docs#3079 (security, P1): a cashier is sale-only. Before this card a
// cashier saw Reports/Settings/Plugins on the Menu grid, Stock on the nav
// rail, and could open /settings, /reports, /inventory, /plugins by URL
// and POST goods-in/stock overrides. This drives a REAL cashier PIN login
// against the auth till (UT_AUTH on) and asserts both halves: the entry
// points are gone and the direct URLs answer 403 with the rail intact
// (Sell stays reachable).
//
// Needs the `auth` project — the default project runs UT_AUTH=off, where
// every permission check passes. Named to sort AFTER login.spec.ts: the auth
// till is shared and serial, and login.spec.ts needs it still first-boot.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `cashier3079${RUN}`;
const CASHIER_PIN = '246813';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Cashier 3079', role: 'cashier' },
  });
  expect(createResp.ok(), 'create cashier user').toBe(true);

  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  expect(pinAction, 'cashier row has a pin-set form').not.toBeNull();
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];

  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginAsCashier(page: Page): Promise<void> {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of CASHIER_PIN.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
  await markTourDone(page); // ut-docs#3710: a new operator would get the guided tour
}

test.describe('Cashier is sale-only (ut-docs#3079)', () => {
  test('no admin entry points, and direct admin URLs are refused', async ({ page }) => {
    await ensureOperator(page); // admin — first-boot wizard or PIN re-login

    // Control: the admin sees the entry points this spec says a cashier
    // must not — so their absence below is the gate, not a broken page.
    await page.goto('/menu');
    for (const href of ['/reports', '/settings', '/plugins']) {
      await expect(page.locator(`main a[href="${href}"]`).first()).toBeVisible();
    }
    await expect(page.locator('[data-testid="kiosk-inventory-link"]')).toHaveCount(1);

    await createCashier(page);
    await loginAsCashier(page);

    // The sale screen: rail has Sell/Menu/Orders but no Stock.
    await page.goto('/');
    await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
    await expect(page.locator('[data-testid="kiosk-inventory-link"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="kiosk-inventory-link-phone"]')).toHaveCount(0);
    // ut-docs#3074 (owner rule): quick-button/category arranging is
    // catalog_management only -- no pen/add link into the Designer.
    await expect(page.locator('.products').first()).toBeVisible();
    await expect(page.locator('[data-testid="products-add-link"]')).toHaveCount(0);
    // ut-docs#2465: moving a quick button to another category is catalog
    // work -- a cashier gets neither the Move to category badge nor its
    // dialog (the badge template itself carries none for a Locked session).
    await expect(page.locator('#tile-move-dialog')).toHaveCount(0);
    await expect(page.locator('#tile-badges-tpl')).toHaveCount(1); // the check below is not vacuous
    expect(await page.locator('#tile-badges-tpl').evaluateAll((els) =>
      els.some((t) => !!(t as HTMLTemplateElement).content.querySelector('.tile-badge-move'))),
    'cashier badge template has no Move to category badge').toBe(false);

    // The Menu grid: sale-flow tiles only.
    await page.goto('/menu');
    await expect(page.locator('main a[href="/journal"]').first()).toBeVisible();
    for (const href of ['/reports', '/settings', '/plugins', '/inventory']) {
      await expect(page.locator(`a[href="${href}"]`)).toHaveCount(0);
    }

    // Direct URLs: 403, the localized refusal, the rail still there.
    for (const path of ['/settings', '/reports', '/inventory', '/plugins', '/plugins/store', '/designer']) {
      const resp = await page.goto(path);
      expect(resp?.status(), `cashier GET ${path}`).toBe(403);
      await expect(page.locator('body')).toContainText('Manager or admin required');
      await expect(page.locator('[data-testid="nav-till"]')).toBeVisible();
    }

    // The stock write that had no check at all (its override sibling was
    // removed in ut-docs#3631).
    for (const path of ['/api/inventory/receipt']) {
      const resp = await page.request.post(path, { form: { item_id: 'x', location_id: 'x', quantity: '1', type: 'receive' } });
      expect(resp.status(), `cashier POST ${path}`).toBe(403);
    }

    // ut-docs#3074: the Designer's category reorder is a plain 403; the
    // quick-button reorder (reachable from the sale screen's jiggle mode,
    // ut-docs#2312) never succeeds without a manager's PIN -- it answers
    // with the elevation prompt, not a 204.
    const catReorder = await page.request.post('/api/designer/categories/reorder', { form: { ids: 'x' } });
    expect(catReorder.status(), 'cashier POST /api/designer/categories/reorder').toBe(403);
    const btnReorder = await page.request.post('/api/buttons/reorder', { form: { codes: 'x' } });
    expect(btnReorder.status(), 'cashier POST /api/buttons/reorder must not succeed').not.toBe(204);
    expect(await btnReorder.text(), 'cashier POST /api/buttons/reorder asks for a manager PIN').toContain('elevation');
    // ut-docs#2465: the same gate for moving an item to another category.
    const btnRecat = await page.request.post('/api/buttons/recategorize', { form: { item_id: 'x', category_id: '' } });
    expect(btnRecat.status(), 'cashier POST /api/buttons/recategorize must not succeed').not.toBe(204);
    expect(await btnRecat.text(), 'cashier POST /api/buttons/recategorize asks for a manager PIN').toContain('elevation');
  });
});
