import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#2905 (owner, 2026-09-25): saving an item or a variant in the
// catalogue editor must be "ajax-like, not a page post" — a targeted swap
// with a success or validation message, never a full reload. The Details
// save already answers with row-level OOB fragments (#1363) and the
// Variants tab re-renders only #catalog-variants (#2815); this pins both,
// on success AND on a server refusal, with a marker on `window` that a
// reload would wipe.

async function createItem(page, name: string, sku: string): Promise<string> {
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('4.00');
  await page.locator('#item-sku').fill(sku);
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
  await closeItemForm(page);
  return (await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id'))!;
}

function mark(page) {
  return page.evaluate(() => { (window as any).__ut2905 = 'still-here'; });
}

async function notReloaded(page) {
  expect(await page.evaluate(() => (window as any).__ut2905), 'page was reloaded').toBe('still-here');
}

test.describe('item editor saves swap in place (ut-docs#2905)', () => {
  test('Details: a save updates the card and says Saved; a refused save says why — no reload', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*400|^Response Status Error Code 400/);
    await page.goto('/catalog');
    const stamp = Date.now();
    const taken = `SKU-2905-A-${stamp}`;
    await createItem(page, `Details 2905 A ${stamp}`, taken);
    const name = `Details 2905 B ${stamp}`;
    const id = await createItem(page, name, `SKU-2905-B-${stamp}`);
    await mark(page);

    // Success: the card is re-rendered from the server, the dialog says Saved.
    const card = page.locator(`#catalog-row-${id}`);
    await card.evaluate((el) => el.setAttribute('data-stale', '1'));
    await card.click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    const renamed = `${name} renamed`;
    await page.locator('#item-name').fill(renamed);
    const [ok] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
      page.locator('#item-form-submit').click(),
    ]);
    expect(ok.status()).toBe(200);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await expect(page.locator(`#catalog-row-${id}[data-stale]`)).toHaveCount(0);
    await expect(page.locator(`#catalog-row-${id}`)).toContainText(renamed);
    await notReloaded(page);
    await closeItemForm(page);

    // Refusal: a SKU another item already uses comes back 400 and the
    // server's reason lands in the dialog, which stays open with the input.
    await page.locator(`#catalog-row-${id}`).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-sku').fill(taken);
    const [bad] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
      page.locator('#item-form-submit').click(),
    ]);
    expect(bad.status()).toBe(400);
    await expect(page.locator('#item-form-msg .pos-notice.error')).toBeVisible();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await expect(page.locator('#item-sku')).toHaveValue(taken);
    await notReloaded(page);
    assertClean();
  });

  test('Variants: add and edit swap only the panel and say Saved; a refused save says why — no reload', async ({ page }) => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*400|^Response Status Error Code 400/);
    await page.goto('/catalog');
    const stamp = Date.now();
    const taken = `SKU-2905-L-${stamp}`;
    const id = await createItem(page, `Variants 2905 ${stamp}`, `SKU-2905-W-${stamp}`);
    await mark(page);

    await page.locator(`#catalog-row-${id}`).click();
    await page.locator('#item-form-tab-variants').click();
    const panel = page.locator('#catalog-variants');
    await expect(panel).toHaveAttribute('data-item-id', id);

    // Add a variant.
    await panel.evaluate((el) => el.setAttribute('data-stale', '1'));
    await page.locator('input[form="vf-new"][name="name"]').fill('Large');
    await page.locator('input[form="vf-new"][name="sku"]').fill(taken);
    await page.locator('input[form="vf-new"].variant-price-major').fill('2.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
      page.locator('button[form="vf-new"][type=submit]').click(),
    ]);
    await expect(page.locator('#catalog-variants[data-stale]')).toHaveCount(0);
    await expect(page.getByTestId('variant-saved')).toBeVisible();
    const row = page.locator('.vg-row:not(.vg-new)');
    await expect(row).toHaveCount(1);
    await notReloaded(page);

    // Edit it.
    await page.locator('#catalog-variants').evaluate((el) => el.setAttribute('data-stale', '1'));
    await row.locator('.variant-price-major').fill('2.50');
    const [ok] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
      row.locator('button[form^="vf-"][type=submit]').click(),
    ]);
    expect(ok.status()).toBe(200);
    await expect(page.locator('#catalog-variants[data-stale]')).toHaveCount(0);
    await expect(page.getByTestId('variant-saved')).toBeVisible();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await notReloaded(page);

    // Refusal: a second variant with the first one's SKU comes back 400;
    // the panel is left as typed and the dialog shows the server's reason.
    await page.locator('#catalog-variants').evaluate((el) => el.setAttribute('data-stale', '1'));
    await page.locator('input[form="vf-new"][name="name"]').fill('Small');
    await page.locator('input[form="vf-new"][name="sku"]').fill(taken);
    await page.locator('input[form="vf-new"].variant-price-major').fill('1.50');
    const [bad] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
      page.locator('button[form="vf-new"][type=submit]').click(),
    ]);
    expect(bad.status()).toBe(400);
    await expect(page.locator('#item-form-msg .pos-notice.error')).toBeVisible();
    await expect(page.locator('#catalog-variants[data-stale]')).toHaveCount(1);
    await expect(page.locator('input[form="vf-new"][name="name"]')).toHaveValue('Small');
    await expect(page.locator('.vg-row:not(.vg-new)')).toHaveCount(1);
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await notReloaded(page);
    assertClean();
  });
});
