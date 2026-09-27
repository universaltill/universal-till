import { test, expect } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#3000: measured on the Android tablet (v0.28.1), a rail tap showed
// nothing for 200-650 ms -- the boosted navigation's page zoom (ADR-0122)
// only starts after the fetched page is swapped in. base.html's shell script
// now marks the pressed navigation target `.ut-pressed` synchronously on
// pointerdown, and the tapped link `.ut-nav-pending` the moment its boosted
// request starts. Both are colour-only (app.css), so they apply under the
// Light effects level and reduced motion too. GET / also paints the basket
// inline (no follow-up /ui/basket request).

test.use({ hasTouch: true });

type CDP = import('@playwright/test').CDPSession;

async function center(page: import('@playwright/test').Page, selector: string) {
  const box = await page.locator(selector).boundingBox();
  if (!box) throw new Error(`no box for ${selector}`);
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

async function touch(cdp: CDP, type: 'touchStart' | 'touchMove' | 'touchEnd', x: number, y: number) {
  await cdp.send('Input.dispatchTouchEvent', {
    type,
    touchPoints: type === 'touchEnd' ? [] : [{ x, y, id: 1, radiusX: 4, radiusY: 4, force: 1 }],
  });
}

const MENU = '[data-testid="nav-menu"]';

test.describe('instant tap feedback on navigation (ut-docs#3000)', () => {
  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    await page.request.post('/api/settings/effects-level', { form: { level: WORKER_TILL_EFFECTS_LEVEL } });
  });

  test('GET / paints the basket inline: no follow-up /ui/basket request', async ({ page }) => {
    const assertClean = watchConsole(page);
    let basketFetches = 0;
    page.on('request', (r) => { if (new URL(r.url()).pathname === '/ui/basket') basketFetches++; });
    const res = await page.goto('/');
    const html = (await res!.text());
    expect(html).toContain('id="basket"');
    expect(html).not.toContain('hx-get="/ui/basket" hx-trigger="load"');
    await page.waitForLoadState('networkidle');
    await expect(page.locator('#basket')).toBeVisible();
    await expect(page.locator('#basket [data-testid="basket-count"]')).toHaveText('0');
    expect(basketFetches, 'the basket is in the first paint').toBe(0);
    assertClean();
  });

  test('pointerdown on the Menu rail link marks it pressed synchronously', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const r = await page.evaluate((sel) => {
      const a = document.querySelector(sel) as HTMLElement;
      const before = a.classList.contains('ut-pressed');
      const rect = a.getBoundingClientRect();
      a.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true, cancelable: true, composed: true, pointerType: 'touch', isPrimary: true, button: 0,
        clientX: rect.left + rect.width / 2, clientY: rect.top + rect.height / 2,
      }));
      // Same task: no frame, no timer has run yet.
      const after = a.classList.contains('ut-pressed');
      window.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, pointerType: 'touch' }));
      return { before, after, cleared: !a.classList.contains('ut-pressed') };
    }, MENU);
    expect(r).toEqual({ before: false, after: true, cleared: true });
  });

  test('a real tap on Menu shows pressed, then pending, while /menu is still loading', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const restBg = await page.locator(MENU).evaluate((el) => getComputedStyle(el).backgroundColor);

    let released = false;
    await page.route((u) => u.pathname === '/menu', async (route) => {
      await new Promise((res) => setTimeout(res, 800));
      released = true;
      await route.continue();
    });

    const cdp = await page.context().newCDPSession(page);
    const p = await center(page, MENU);
    await touch(cdp, 'touchStart', p.x, p.y);
    const pressed = await page.locator(MENU).evaluate((el) => ({
      cls: el.classList.contains('ut-pressed'),
      bg: getComputedStyle(el).backgroundColor,
      transform: getComputedStyle(el).transform,
    }));
    expect(pressed.cls, 'pressed on touch down').toBe(true);
    expect(pressed.bg, 'pressed is a colour change').not.toBe(restBg);
    expect(pressed.transform).toBe('none');
    await touch(cdp, 'touchEnd', p.x, p.y);

    // The request is in flight (held 800 ms by the route): the tapped rail
    // item is already marked, on the still-visible sale screen.
    await expect(page.locator(MENU)).toHaveClass(/(^|\s)ut-nav-pending(\s|$)/, { timeout: 500 });
    expect(released, 'asserted before /menu answered').toBe(false);
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.locator('#basket')).toBeVisible();
    const pendingBg = await page.locator(MENU).evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(pendingBg).not.toBe(restBg);

    await expect(page).toHaveURL(/\/menu$/);
    // The new page's fresh rail carries neither marker.
    await expect(page.locator('.nav .ut-nav-pending, .nav .ut-pressed')).toHaveCount(0);
    assertClean();
  });

  test('a failed navigation does not leave the pending marker behind', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.route((u) => u.pathname === '/menu', (route) => route.abort());
    await page.locator(MENU).click();
    await expect(page.locator(MENU)).not.toHaveClass(/ut-nav-pending/);
    expect(new URL(page.url()).pathname).toBe('/');
  });

  test('a touch pan starting on a rail link never leaves it pressed', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const cdp = await page.context().newCDPSession(page);
    const p = await center(page, MENU);
    await touch(cdp, 'touchStart', p.x, p.y);
    await expect(page.locator(MENU)).toHaveClass(/ut-pressed/);
    await touch(cdp, 'touchMove', p.x, p.y + 6);
    await touch(cdp, 'touchMove', p.x, p.y + 40);
    await expect(page.locator(MENU), 'moved past the slop: no longer pressed').not.toHaveClass(/ut-pressed/);
    await touch(cdp, 'touchEnd', p.x, p.y + 40);
    await page.waitForTimeout(150);
    await expect(page.locator(MENU)).not.toHaveClass(/ut-pressed|ut-nav-pending/);
    expect(new URL(page.url()).pathname).toBe('/');
  });

  test('Light effects level: the pressed state still applies, colour only', async ({ page }) => {
    const res = await page.request.post('/api/settings/effects-level', { form: { level: 'light' } });
    expect(res.status()).toBe(204);
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await expect(page.locator('html')).toHaveClass(/(^|\s)fx-light(\s|$)/);
    const restBg = await page.locator(MENU).evaluate((el) => getComputedStyle(el).backgroundColor);
    const cdp = await page.context().newCDPSession(page);
    const p = await center(page, MENU);
    await touch(cdp, 'touchStart', p.x, p.y);
    const s = await page.locator(MENU).evaluate((el) => ({
      cls: el.classList.contains('ut-pressed'),
      bg: getComputedStyle(el).backgroundColor,
      transform: getComputedStyle(el).transform,
      anims: el.getAnimations().length,
    }));
    await touch(cdp, 'touchMove', p.x, p.y + 40);
    await touch(cdp, 'touchEnd', p.x, p.y + 40);
    expect(s.cls).toBe(true);
    expect(s.bg).not.toBe(restBg);
    expect(s.transform).toBe('none');
    expect(s.anims).toBe(0);
  });
});

// Review of #3000: the per-tile hx-on opener became one delegated
// htmx:afterRequest listener. When the tile is swapped out while its picker
// request is in flight (a grid refetch), htmx re-fires afterRequest on a
// surviving ancestor, so the listener must match the ISSUER
// (requestConfig.elt), or the picker lands in #modifier-modal unopened.
test('a modifier tile swapped out mid-request still opens its picker', async ({ page }) => {
  const tag = `${Date.now().toString(36)}`;
  const name = `Tf3000 Mod ${tag}`;
  const barcode = `TF3000BC-${tag}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},TF3000${tag},${barcode},1.00,Tf3000 Cat,1\n`;
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-tf3000-${tag}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  const itemId = (await page.locator(`.catalog-row[data-name="${name}"]`).first().getAttribute('data-id'))!;
  expect((await page.request.post('/api/buttons/add', { form: { itemId, label: name, code: barcode } })).ok()).toBe(true);
  expect((await page.request.post('/api/catalog/modifier-group', { form: { itemId, name: `Size ${tag}`, minSelect: '0', maxSelect: '1' } })).ok()).toBe(true);
  const panel = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`)).text();
  const at = panel.indexOf(`>Size ${tag}<`);
  const ids = [...panel.slice(0, at).matchAll(/data-group-id="([^"]*)"/g)];
  expect((await page.request.post('/api/catalog/modifier-option', { form: { groupId: ids[ids.length - 1][1], itemId, name: `Large ${tag}` } })).ok()).toBe(true);

  await page.goto('/');
  const tile = page.locator('.btn-tile:visible', { hasText: name });
  await expect(tile).toBeVisible();
  // Hold the picker response while the grid re-renders under it.
  await page.route('**/ui/pos/modifiers**', async (route) => {
    await new Promise((r) => setTimeout(r, 900));
    await route.continue();
  });
  await tile.evaluate((el) => el.setAttribute('data-tf3000-old', '1'));
  await tile.click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/ui/buttons') && !r.url().includes('/version')),
    page.evaluate(() => (window as any).htmx.trigger(document.body, 'buttons-changed')),
  ]);
  expect(await page.locator('[data-tf3000-old]').count(), 'the tapped tile was replaced by the refetch').toBe(0);
  await expect(page.locator('#modifier-modal')).toHaveJSProperty('open', true, { timeout: 5000 });
});
