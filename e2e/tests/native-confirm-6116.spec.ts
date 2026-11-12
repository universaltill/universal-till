import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, clickThenReload, answerTillConfirm, noNativeDialogs } from './helpers';

// ut-docs#6116: the last native window.confirm sites now ask in the till's
// own dialog (#ut-confirm, web/public/till-confirm.js), so the status bar and
// Lock stay reachable. Covers the record-dialog discard guard (Close, Escape,
// New over a dirty edit), Lock while asking, the missing-#ut-confirm native
// fallback (never a silent discard) and /plugins Uninstall / Roll back.
// The catalog, bug panel and store-detail sites are covered in their own
// converted specs (catalog-modifiers-goto-guard-2379,
// bugreport-panel-persists-2342, catalog-store-detail-actions-4228).

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const DISCARD = 'Discard your unsaved changes?';

async function createCategory(page: Page, name: string) {
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator(NAME).fill(name);
  await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
  await expect(page.locator('#categories-table .category-row', { hasText: name })).toHaveCount(1);
}

const row = (page: Page, name: string) =>
  page.locator('#categories-table .category-row', { hasText: name }).first();
const confirmDlg = (page: Page) => page.getByTestId('till-confirm');
const focusInDialog = (page: Page) =>
  page.evaluate((sel) => !!document.querySelector(sel)?.contains(document.activeElement), DIALOG);

test.describe('record dialog discard guard asks in the till dialog (ut-docs#6116)', () => {
  test('(a) a clean Close, Escape and New act at once with no question', async ({ page }) => {
    const none = noNativeDialogs(page);
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await expect(dlg).toBeVisible();
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    await expect(confirmDlg(page)).toBeHidden();
    await page.locator('#categories-new').click();
    await page.locator(NAME).press('Escape');
    await expect(dlg).toBeHidden();
    await expect(confirmDlg(page)).toBeHidden();
    none();
    assertClean();
  });

  test('(b) dirty Close: Cancel keeps the text and focus in the dialog; Escape in the question is Cancel', async ({ page }) => {
    const none = noNativeDialogs(page);
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('unsaved 6116');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await answerTillConfirm(page, 'cancel', DISCARD);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('unsaved 6116');
    expect(await focusInDialog(page)).toBe(true);

    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await answerTillConfirm(page, 'escape', DISCARD);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('unsaved 6116');
    expect(await focusInDialog(page)).toBe(true);
    none();
    assertClean();
  });

  test('(b2) dirty Escape from a field: Cancel returns focus to the field, Confirm closes', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.goto('/categories');
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('esc 6116');
    await page.locator(NAME).press('Escape');
    await answerTillConfirm(page, 'cancel', DISCARD);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toBeFocused();
    await page.locator(NAME).press('Escape');
    await answerTillConfirm(page, 'confirm', DISCARD);
    await expect(dlg).toBeHidden();
    none();
  });

  test('(c)(g) dirty Close + Confirm hides it, focus returns to the row, re-open shows the saved value', async ({ page }) => {
    const none = noNativeDialogs(page);
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Discard Probe ' + Date.now();
    await createCategory(page, name);
    const dlg = page.locator(DIALOG);
    await row(page, name).locator('[data-record-edit]').click();
    await expect(dlg).toBeVisible();
    await page.locator(NAME).fill(name + ' edited');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await answerTillConfirm(page, 'confirm', DISCARD);
    await expect(dlg).toBeHidden();
    await expect(row(page, name).locator('[data-record-edit]')).toBeFocused();

    await row(page, name).locator('[data-record-edit]').click();
    await expect(page.locator(NAME)).toHaveValue(name);
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    await expect(confirmDlg(page)).toBeHidden();
    none();
    assertClean();
  });

  test('(e) dirty edit + New: Cancel keeps the edit, Confirm opens a blank create form', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.goto('/categories');
    const name = 'New Over Dirty ' + Date.now();
    await createCategory(page, name);
    const dlg = page.locator(DIALOG);
    await row(page, name).locator('[data-record-edit]').click();
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await page.locator(NAME).fill(name + ' edited');

    // The opaque dialog covers New, so dispatch the click as a keyboard
    // activation would (same reasoning as categories-record-dialog-2010 c2).
    await page.locator('#categories-new').dispatchEvent('click');
    await answerTillConfirm(page, 'cancel', DISCARD);
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await expect(page.locator(NAME)).toHaveValue(name + ' edited');
    expect(await focusInDialog(page)).toBe(true);

    await page.locator('#categories-new').dispatchEvent('click');
    await answerTillConfirm(page, 'confirm', DISCARD);
    await expect(dlg).toHaveAttribute('data-record-mode', 'create');
    await expect(page.locator(NAME)).toHaveValue('');
    await expect(page.locator(`${DIALOG} [data-record-dialog-destructive]`)).toBeHidden();
    none();
  });

  test('(f) a second Close tap while asking is ignored: one question, dialog still open', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.goto('/categories');
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('twice 6116');
    const close = page.locator(`${DIALOG} .record-dialog-close`);
    await close.dispatchEvent('click');
    await expect(confirmDlg(page)).toBeVisible();
    await close.dispatchEvent('click');
    await close.dispatchEvent('click');
    await expect(page.locator('dialog#ut-confirm[open]')).toHaveCount(1);
    await expect(dlg).toBeVisible();
    await answerTillConfirm(page, 'cancel', DISCARD);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('twice 6116');
    none();
  });

  test('AC12 Lock stays clickable while the question is open and the edit is kept as Cancel', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.goto('/categories');
    let logouts = 0;
    await page.route('**/api/auth/logout', async (route) => {
      logouts++;
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<p>locked</p>' });
    });
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('lock 6116');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(confirmDlg(page)).toBeVisible();
    // A real (hit-tested) click: nothing covers Lock while the question is up.
    await page.locator(`${DIALOG} [data-record-dialog-lock] button`).click();
    await expect.poll(() => logouts).toBe(1);
    none();
  });

  test('AC13 with #ut-confirm missing the guard still asks (natively): Cancel keeps the edit', async ({ page }) => {
    // Deliberately NOT noNativeDialogs: this is the one case that must go native.
    await page.goto('/categories');
    await page.evaluate(() => document.getElementById('ut-confirm')!.remove());
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('fallback 6116');
    const asked: string[] = [];
    page.once('dialog', (d) => { asked.push(d.message()); void d.dismiss(); });
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect.poll(() => asked.length).toBe(1);
    expect(asked[0]).toBe(DISCARD);
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('fallback 6116');
  });
});

// /plugins: the default till has no installed plugin, so the page's embedded
// JSON is rewritten to one fake item and every plugin API call is answered
// locally; nothing here reaches the real server.
test.describe('/plugins Uninstall and Roll back ask in the till dialog (ut-docs#6116)', () => {
  const PLUGIN = { id: 'fake-6116', name: 'Fake Plugin 6116', version: '2.0.0', enabled: true, trust: 'verified',
    state: 'installed', hasUpdate: false, latest: '', versionUnknown: false, docsRoute: '' };

  async function setup(page: Page) {
    await page.route('**/plugins', async (route) => {
      if (route.request().method() !== 'GET' || route.request().resourceType() !== 'document') return route.fallback();
      const res = await route.fetch();
      const html = (await res.text()).replace(
        /(<script id="plugins-data" type="application\/json">)[\s\S]*?(<\/script>)/,
        `$1${JSON.stringify({ items: [PLUGIN], q: '' })}$2`);
      await route.fulfill({ response: res, body: html, headers: { ...res.headers(), 'content-length': String(Buffer.byteLength(html)) } });
    });
    const hits = { uninstall: [] as (string | null)[], rollback: [] as (string | null)[] };
    await page.route('**/api/plugins/fake-6116/uninstall', async (route) => {
      hits.uninstall.push(route.request().postData());
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"data":{},"error":null}' });
    });
    await page.route('**/api/plugins/fake-6116/rollback', async (route) => {
      hits.rollback.push(route.request().postData());
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"data":{},"error":null}' });
    });
    await page.route('**/api/plugins/fake-6116/versions', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json',
        body: JSON.stringify({ data: { versions: [{ version: '1.0.0' }] }, error: null }) });
    });
    return hits;
  }

  test('Uninstall: Cancel posts nothing, Confirm posts once; the question names the plugin', async ({ page }) => {
    const none = noNativeDialogs(page);
    const hits = await setup(page);
    await page.goto('/plugins');
    const btn = page.getByRole('button', { name: 'Uninstall' });
    await btn.click();
    await answerTillConfirm(page, 'cancel', PLUGIN.name);
    await expect(btn).toBeFocused();
    expect(hits.uninstall).toHaveLength(0);
    await btn.click();
    await answerTillConfirm(page, 'confirm', PLUGIN.name);
    await expect.poll(() => hits.uninstall.length).toBe(1);
    none();
  });

  test('Roll back: the question names plugin and version; Cancel posts nothing, Confirm posts once', async ({ page }) => {
    const none = noNativeDialogs(page);
    const hits = await setup(page);
    await page.goto('/plugins');
    await page.getByRole('button', { name: 'Versions' }).click();
    const rb = page.locator('button', { hasText: /roll back/i });
    await expect(rb).toHaveCount(1);
    await rb.click();
    await answerTillConfirm(page, 'cancel', new RegExp(`${PLUGIN.name} 1\\.0\\.0`));
    expect(hits.rollback).toHaveLength(0);
    await rb.click();
    await answerTillConfirm(page, 'confirm', new RegExp(`${PLUGIN.name} 1\\.0\\.0`));
    await expect.poll(() => hits.rollback.length).toBe(1);
    expect(JSON.parse(hits.rollback[0] || '{}')).toEqual({ version: '1.0.0' });
    none();
  });
});
