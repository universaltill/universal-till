import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1363: catalog admin mutations answer with just the affected row as
// HTMX out-of-band swaps instead of refetching + re-rendering the entire
// unbounded item/barcode/variant/tax-code table per mutation. This drives
// the full row lifecycle in a real browser — create (OOB beforeend insert),
// edit (OOB replace-in-place), deactivate (OOB delete) — asserting both the
// visible outcome AND that each mutation response really is row-scoped (no
// full-table payload riding along).
//
// watchConsole is a general no-JS-error net (htmx 1.9.12 swaps a missing OOB
// target silently, so it isn't specifically an OOB-correctness check) — a
// clean console is still part of the contract for a change riding entirely
// on new client-side swap wiring.
test('catalog mutations swap just the affected row, not the whole table', async ({ page }) => {
  const assertClean = watchConsole(page);

  // Every mutation this spec performs answers row-scoped: collect any
  // mutation response that still carries the full table.
  const fullTableResponses: string[] = [];
  page.on('response', async (res) => {
    const url = res.url();
    if (!url.includes('/api/catalog/')) return;
    if (res.request().method() !== 'POST') return;
    try {
      const body = await res.text();
      if (body.includes('id="catalog-table"')) fullTableResponses.push(url);
    } catch {
      /* response body already gone — navigation; irrelevant here */
    }
  });

  await page.goto('/catalog');
  const rows = page.locator('#catalog-rows .catalog-row');
  const rowCountBefore = await rows.count();
  expect(rowCountBefore).toBeGreaterThan(0); // demo catalog is seeded

  // CREATE: the new row is OOB-appended into #catalog-rows.
  const name = 'OOB Probe ' + Date.now();
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('2.50');
  await page.locator('#item-form-submit').click();
  const newRow = page.locator('#catalog-rows .catalog-row', { hasText: name });
  await expect(newRow).toBeVisible();
  await expect(rows).toHaveCount(rowCountBefore + 1);

  // EDIT: the row is replaced in place — same position, same total count,
  // updated content, and the row keeps its stable id (the OOB anchor).
  const rowId = await newRow.getAttribute('id');
  expect(rowId).toMatch(/^catalog-row-/);
  await newRow.locator('td').nth(1).click();
  await expect(page.locator('#item-id')).not.toHaveValue('');
  const renamed = name + ' v2';
  await page.locator('#item-name').fill(renamed);
  await page.locator('#item-form-submit').click();
  const editedRow = page.locator('#catalog-rows .catalog-row', { hasText: renamed });
  await expect(editedRow).toBeVisible();
  await expect(editedRow).toHaveAttribute('id', rowId!);
  await expect(rows).toHaveCount(rowCountBefore + 1);

  // DEACTIVATE: the row is OOB-deleted — the rest of the table survives
  // untouched (a full-table swap would have re-rendered everything).
  page.once('dialog', (d) => d.accept());
  await editedRow.locator('button.danger').click();
  await expect(page.locator('#catalog-rows .catalog-row', { hasText: name })).toHaveCount(0);
  await expect(rows).toHaveCount(rowCountBefore);

  expect(
    fullTableResponses,
    'every catalog mutation must answer row-scoped, never with the full table',
  ).toEqual([]);
  assertClean();
});
