import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1363: catalog mutations answer with row-level HTMX out-of-band
// fragments — one row inserted/updated/removed — instead of re-rendering
// and outerHTML-swapping the ENTIRE items table after every mutation.
//
// The user-visible property under test is DOM identity: the table element
// and every sibling row must be the SAME nodes before and after a
// mutation (a full-table swap detaches all of them, losing scroll
// position, focus and any transient state). Identity is proven by
// stamping a marker attribute on the live elements from the page context
// — a re-rendered replacement can never carry it — plus isConnected on a
// captured element handle.
//
// watchConsole is the general no-JS-error harness convention (an
// over-emitted OOB delete is actually a silent no-op in the vendored htmx
// 1.9.12 — its "missing target" branch is unreachable — so it would NOT
// fail these tests on its own; the real protocol assertions below are
// what pin insert/update/delete correctness).
test.describe('catalog row-level OOB swaps (ut-docs#1363)', () => {
  async function createItem(page: import('@playwright/test').Page, name: string) {
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await expect(page.locator(`.catalog-row[data-name="${name}"]`)).toBeVisible();
  }

  test('creating an item inserts its row without replacing the table or existing rows', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // Stamp identity markers on the live table and the first existing row.
    const sibling = page.locator('#catalog-table .catalog-row').first();
    await expect(sibling).toBeVisible();
    const siblingHandle = await sibling.elementHandle();
    await page.evaluate(() => {
      (document.getElementById('catalog-table') as HTMLElement).setAttribute('data-e2e-identity', 'original-table');
    });
    await siblingHandle!.evaluate((el) => el.setAttribute('data-e2e-identity', 'original-row'));

    const name = 'Row OOB Insert ' + Date.now();
    await createItem(page, name);

    // The new row landed in the SAME tbody of the SAME table element…
    await expect(page.locator('#catalog-table[data-e2e-identity="original-table"]')).toHaveCount(1);
    await expect(page.locator(`#catalog-tbody .catalog-row[data-name="${name}"]`)).toHaveCount(1);
    // …and the pre-existing sibling row is still the very same node.
    expect(await siblingHandle!.evaluate((el) => el.isConnected)).toBe(true);
    expect(await siblingHandle!.evaluate((el) => el.getAttribute('data-e2e-identity'))).toBe('original-row');

    assertClean();
  });

  // Review finding (ut-docs#1363): the beforeend insert lands a new row at
  // the BOTTOM of the tbody, which is only correct by accident if the
  // catalog happens to already end alphabetically there — for a real,
  // populated catalog it doesn't, and a shop owner adding an item would
  // find it nowhere near where they'd look for it. The client-side
  // sortRows() (htmx:oobAfterSwap listener) must restore name order
  // without losing the row's DOM identity (appendChild moves, not clones).
  test('an inserted row lands in its name-sorted position, not just appended at the bottom', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // A name guaranteed to sort before the demo catalog's existing items
    // (all start with a capital letter — "0" sorts before any of them).
    const name = '0-Row OOB Sort ' + Date.now();
    await createItem(page, name);

    const newRow = page.locator(`.catalog-row[data-name="${name}"]`);
    const rowHandle = await newRow.elementHandle();

    // It must be the FIRST row in the tbody, not the last child appended.
    const isFirst = await page.evaluate((id) => {
      const tbody = document.getElementById('catalog-tbody')!;
      const first = tbody.querySelector('.catalog-row');
      return first?.getAttribute('data-id') === id;
    }, await newRow.getAttribute('data-id'));
    expect(isFirst).toBe(true);

    // Still the same node (sortRows() must MOVE, not replace, the row).
    expect(await rowHandle!.evaluate((el) => el.isConnected)).toBe(true);

    assertClean();
  });

  test('editing an item replaces only its own row in place', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const name = 'Row OOB Edit ' + Date.now();
    await createItem(page, name);

    // Mark the table and a DIFFERENT (sibling) row before the edit.
    const sibling = page.locator('#catalog-table .catalog-row').first();
    const siblingName = await sibling.getAttribute('data-name');
    expect(siblingName).not.toBe(name);
    const siblingHandle = await sibling.elementHandle();
    await siblingHandle!.evaluate((el) => el.setAttribute('data-e2e-identity', 'sibling-row'));
    await page.evaluate(() => {
      (document.getElementById('catalog-table') as HTMLElement).setAttribute('data-e2e-identity', 'original-table');
    });

    // Click a plain cell (not a .btn) to load the item into the edit form.
    await page.locator(`.catalog-row[data-name="${name}"]`).locator('td').nth(1).click();
    await expect(page.locator('#item-id')).not.toHaveValue('');
    await page.locator('#item-description').fill('edited via row OOB');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    // The edited row was swapped in place (its data-* payload refreshed)…
    await expect(page.locator(`.catalog-row[data-name="${name}"]`))
      .toHaveAttribute('data-description', 'edited via row OOB');
    // …inside the untouched table, with the sibling node intact.
    await expect(page.locator('#catalog-table[data-e2e-identity="original-table"]')).toHaveCount(1);
    expect(await siblingHandle!.evaluate((el) => el.isConnected)).toBe(true);

    assertClean();
  });

  test('deactivating an item removes only its row, leaving siblings attached', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // Two fresh items so the deactivation never eats the shared demo
    // catalog other specs scan.
    const stamp = Date.now();
    const doomed = 'Row OOB Doomed ' + stamp;
    const survivor = 'Row OOB Survivor ' + stamp;
    await createItem(page, doomed);
    await createItem(page, survivor);

    const survivorHandle = await page.locator(`.catalog-row[data-name="${survivor}"]`).elementHandle();
    await survivorHandle!.evaluate((el) => el.setAttribute('data-e2e-identity', 'survivor-row'));
    await page.evaluate(() => {
      (document.getElementById('catalog-table') as HTMLElement).setAttribute('data-e2e-identity', 'original-table');
    });

    // The deactivate button asks via hx-confirm — accept it.
    page.once('dialog', (d) => d.accept());
    await page.locator(`.catalog-row[data-name="${doomed}"]`).locator('button.danger').click();

    // The doomed row is gone (OOB delete fragment)…
    await expect(page.locator(`.catalog-row[data-name="${doomed}"]`)).toHaveCount(0);
    // …while the survivor is the SAME element, still attached, in the
    // same never-replaced table.
    expect(await survivorHandle!.evaluate((el) => el.isConnected)).toBe(true);
    expect(await survivorHandle!.evaluate((el) => el.getAttribute('data-e2e-identity'))).toBe('survivor-row');
    await expect(page.locator('#catalog-table[data-e2e-identity="original-table"]')).toHaveCount(1);

    assertClean();
  });

  test('a row created while a search filter is active respects the filter', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    // Type a filter that will NOT match the item about to be created —
    // the htmx:oobAfterSwap re-trigger must hide the new row immediately.
    await page.locator('#catalog-search').fill('zzz-no-such-item');
    const name = 'Row OOB Filtered ' + Date.now();
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    const newRow = page.locator(`.catalog-row[data-name="${name}"]`);
    await expect(newRow).toHaveCount(1);
    await expect(newRow).toBeHidden();

    // Clearing the search reveals it.
    await page.locator('#catalog-search').fill('');
    await expect(newRow).toBeVisible();

    assertClean();
  });

  // The panel path is the one with NO explicit filterRows() call anywhere
  // in its flow (the item form and image upload both call it from their
  // own .then handlers) — only the htmx:oobAfterSwap listener re-applies
  // the filter when a panel mutation OOB-replaces a hidden row with a
  // fresh (default-visible) element.
  test('a panel mutation on a filtered-out item keeps its refreshed row hidden', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');

    const name = 'Row OOB Panel ' + Date.now();
    await createItem(page, name);

    // Open the item's variants panel, then hide the row with a filter.
    await page.locator(`.catalog-row[data-name="${name}"]`).locator('td').nth(1).click();
    await expect(page.locator('#vf-new')).toBeAttached();
    const row = page.locator(`.catalog-row[data-name="${name}"]`);
    await page.locator('#catalog-search').fill('zzz-no-such-item');
    await expect(row).toBeHidden();

    // Add a variant through the panel — the response carries the panel
    // plus the item's row as an OOB replacement.
    await page.locator('input[form="vf-new"][name="name"]').fill('330ml');
    await page.locator('input.variant-price-major[form="vf-new"]').fill('1.20');
    await page.locator('button[form="vf-new"]').click();

    // The row really was refreshed (its summary now lists the variant)…
    await expect(row).toContainText('330ml');
    // …and the refreshed element still respects the active filter.
    await expect(row).toBeHidden();

    await page.locator('#catalog-search').fill('');
    await expect(row).toBeVisible();

    assertClean();
  });
});
