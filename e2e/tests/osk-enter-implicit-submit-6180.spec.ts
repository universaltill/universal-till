import { test, expect } from './fixtures';
import { setOskMode } from './helpers';

// ut-docs#6180: the OSK's ↵ key called form.requestSubmit() on any form,
// bypassing the HTML implicit-submission rule a physical keyboard's Enter
// obeys. Settings -> Receipt designer's #rd-form has several text fields, no
// submit button and an hx-trigger that omits `submit`, so ↵ did a native GET
// to the same URL: a full reload that dropped every unsaved Header/Footer edit.

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('OSK enter in a receipt-designer field closes the keyboard without reloading', async ({ page }) => {
  await setOskMode(page, 'on');
  await page.goto('/receipt-designer');

  const header = page.locator('#rd-form input[name="header1"]');
  await header.fill('');
  await header.click();
  await expect(page.locator('#osk')).toBeVisible();

  await page.evaluate(() => { (window as any).__marker6180 = 'alive'; });
  const urlBefore = page.url();

  for (const k of ['a', 'b']) {
    await page.locator(`#osk button[data-k="${k}"]`).click();
  }
  await expect(header).toHaveValue('ab');

  await page.locator('#osk button[data-k="↵"]').click();
  // Give a (wrongly) triggered native GET time to commit.
  await page.waitForTimeout(500);

  expect(await page.evaluate(() => (window as any).__marker6180)).toBe('alive');
  expect(page.url()).toBe(urlBefore);
  await expect(header).toHaveValue('ab');
  await expect(page.locator('#osk')).toBeHidden();
});

// The scan form has a submit button, so this pins the default-button branch.
test('OSK enter in the scan form still submits through its submit button', async ({ page }) => {
  await setOskMode(page, 'on');
  await page.goto('/');
  const input = page.locator('form.scan-row input[name="code"]');
  await input.click();
  await expect(page.locator('#osk')).toBeVisible();
  await page.locator('#osk button[data-k="1"]').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('#osk button[data-k="↵"]').click(),
  ]);
});

// Reviewer addition: the third branch of implicitSubmit (no default button,
// exactly one implicit-blocking field) must still requestSubmit(), as a
// physical Enter does. The scan form minus its submit button, with its qty
// number field turned hidden (number also blocks implicit submission), is
// that shape; htmx's hx-post fires on the resulting submit event.
test('OSK enter in a single-field form with no submit button still submits', async ({ page }) => {
  await setOskMode(page, 'on');
  try {
    await page.goto('/');
    await page.evaluate(() => {
      document.querySelectorAll('form.scan-row button[type=submit], form.scan-row button:not([type])')
        .forEach((b) => b.remove());
      (document.querySelector('form.scan-row input[name="qty"]') as HTMLInputElement).type = 'hidden';
    });
    const input = page.locator('form.scan-row input[name="code"]');
    await input.click();
    await expect(page.locator('#osk')).toBeVisible();
    await page.locator('#osk button[data-k="1"]').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan'), { timeout: 10_000 }),
      page.locator('#osk button[data-k="↵"]').click(),
    ]);
  } finally {
    await page.request.post('/api/pos/reset');
  }
});

// --- Tester additions (ut-docs#6180) ---------------------------------------

async function oskType(page: import('@playwright/test').Page, text: string) {
  for (const k of text) await page.locator(`#osk button[data-k="${k}"]`).click();
}

async function openNewCategory(page: import('@playwright/test').Page) {
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await expect(page.locator('#category-dialog')).toBeVisible();
  const name = page.locator('#category-form input[name="name"]');
  await name.fill('');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  return name;
}

// The record dialog's Save is <button type="submit" form="category-form">
// OUTSIDE the form (partials/record_dialog.html). It is still the form's
// default button (form.elements), so ↵ must submit through it, exactly as a
// physical Enter does.
test('OSK enter in a record-dialog form submits through its external Save button', async ({ page }) => {
  await setOskMode(page, 'on');
  await openNewCategory(page);
  await oskType(page, 'q6180');
  let posted = false;
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/categories') posted = true;
  });
  // A successful create answers HX-Redirect to /categories (a real load).
  await Promise.all([
    page.waitForEvent('load'),
    page.locator('#osk button[data-k="↵"]').click(),
  ]);
  expect(posted, '↵ must POST the category form via its default (Save) button').toBe(true);
  await expect(page.locator('#categories-table .category-row', { hasText: 'q6180' })).toHaveCount(1);
});

// A disabled default button blocks implicit submission (HTML spec): ↵ must
// neither POST nor fall through to a native GET reload. Covered for the
// `disabled` attribute and for the real main-till-lock shape, where Save is
// disabled only by its ancestor fieldset[data-main-till-lock] (so
// button.disabled reads false while the button is still actually disabled).
for (const how of ['attribute', 'fieldset'] as const) {
  test(`OSK enter does nothing when the default button is disabled (${how})`, async ({ page }) => {
    await setOskMode(page, 'on');
    const name = await openNewCategory(page);
    await page.evaluate((h) => {
      const save = document.querySelector('#category-dialog .record-dialog-save') as HTMLButtonElement;
      if (h === 'attribute') save.disabled = true;
      else (save.closest('fieldset[data-main-till-lock]') as HTMLFieldSetElement).disabled = true;
      (window as any).__marker6180 = 'alive';
    }, how);
    await expect(page.locator('#category-dialog .record-dialog-save')).toBeDisabled();
    await oskType(page, 'zz');
    let requested = false;
    page.on('request', (r) => {
      const p = new URL(r.url()).pathname;
      if (p === '/api/categories' || (r.isNavigationRequest() && p === '/categories')) requested = true;
    });
    const urlBefore = page.url();
    await page.locator('#osk button[data-k="↵"]').click();
    await page.waitForTimeout(500);
    expect(requested, 'a disabled default button must block ↵ entirely').toBe(false);
    expect(await page.evaluate(() => (window as any).__marker6180)).toBe('alive');
    expect(page.url()).toBe(urlBefore);
    await expect(name).toHaveValue('zz');
    await expect(page.locator('#category-dialog')).toBeVisible();
    await expect(page.locator('#osk')).toBeHidden();
  });
}

// #split-tender-form (sale screen payment overlay, Split tab): amount +
// voucher_id text fields, Add/Complete are type=button. Pre-fix ↵ did a
// native GET of "/?method=…&amount=…": a reload that dropped the overlay
// and its pending payments. Now ↵ only closes the keyboard.
test('OSK enter in the split-tender amount field does not reload the sale screen', async ({ page }) => {
  await setOskMode(page, 'on');
  try {
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.locator('form.scan-row input[name="code"]').fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');
    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();

    const amount = page.locator('#split-tender-form input[name="amount"]');
    await amount.fill('');
    await amount.click();
    await expect(page.locator('#osk')).toBeVisible();
    await page.evaluate(() => { (window as any).__marker6180 = 'alive'; });
    const urlBefore = page.url();
    await oskType(page, '1');
    await expect(amount).toHaveValue('1');

    await page.locator('#osk button[data-k="↵"]').click();
    await page.waitForTimeout(500);

    expect(await page.evaluate(() => (window as any).__marker6180)).toBe('alive');
    expect(page.url()).toBe(urlBefore);
    await expect(amount).toHaveValue('1');
    await expect(amount).toBeVisible();
    await expect(page.locator('#osk')).toBeHidden();
  } finally {
    await page.request.post('/api/pos/reset');
  }
});
