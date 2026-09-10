import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2011: tapping a stock (or low-stock) row used to just scroll-and-
// fill a form sitting further down the page -- easy to miss, per the
// product owner's own report ("the form is at the end"). This covers the
// full-screen popup that replaces that: prefilled from the tapped row,
// following the ut-docs#2010 dialog standard (full-screen, explicit close,
// Escape dismisses) but driven by this page's own script rather than the
// shared record_dialog engine (this endpoint is an htmx swap-in-place, not
// a plain-POST-redirect one -- see inventory.html's own comment).

const DIALOG = '#stock-dialog';

test.describe('inventory stock-row dialog (ut-docs#2011)', () => {
  test('(a) tapping a stock row opens the dialog prefilled, with quantity left blank', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeHidden();

    const row = page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' }).first();
    await row.locator('td').first().click();

    await expect(dlg).toBeVisible();
    // Opened with show(), not showModal() -- the OSK must stay reachable
    // (ut-docs#1385), same reasoning/assertion as the ut-docs#2010 dialog.
    expect(await dlg.evaluate((d) => d.matches(':modal'))).toBe(false);

    await expect(page.locator('#stock-item-id')).not.toHaveValue('');
    await expect(page.locator('#stock-item-search')).toHaveValue(/Pepsi Can 330ml/);
    // Plain-language confirmation of what is about to change (AC: must not
    // make it easy to adjust the wrong item).
    const ctx = page.locator('#stock-dialog-context');
    await expect(ctx).toBeVisible();
    await expect(ctx).toContainText('Pepsi Can 330ml');

    // Never prefilled: a bare Save on a freshly-opened dialog cannot apply
    // a change.
    await expect(page.locator('#stock-form input[name="quantity"]')).toHaveValue('');
    assertClean();
  });

  test('(b) the header "Add stock" button opens the dialog blank, for an item not in the visible list', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    const dlg = page.locator(DIALOG);

    const addBtn = page.locator('#stock-dialog-open');
    await expect(addBtn).toHaveAttribute('aria-label', /.+/);
    await expect(addBtn).toHaveAttribute('title', /.+/);
    await expect(addBtn).toHaveText(''); // icon-only

    await addBtn.click();
    await expect(dlg).toBeVisible();
    await expect(page.locator('#stock-item-search')).toHaveValue('');
    await expect(page.locator('#stock-item-id')).toHaveValue('');
    await expect(page.locator('#stock-dialog-context')).toBeHidden();
    assertClean();
  });

  test('(c) Escape and the explicit Close button both dismiss the dialog', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    const dlg = page.locator(DIALOG);

    await page.locator('#stock-dialog-open').click();
    await expect(dlg).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dlg).toBeHidden();

    await page.locator('#stock-dialog-open').click();
    await expect(dlg).toBeVisible();
    await page.locator('#stock-dialog-close').click();
    await expect(dlg).toBeHidden();
    assertClean();
  });

  test('(d) submitting updates the stock table in place, with no full page reload, and the dialog stays open showing the result', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');

    const row = page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' }).first();
    const startingQty = (await row.count()) > 0
      ? parseFloat((await row.locator('td').nth(3).innerText()).trim())
      : 0;

    await row.locator('td').first().click();
    await page.locator('#stock-form input[name="quantity"]').fill('3');
    await page.locator('#stock-form input[name="reason"]').fill('e2e: ut-docs#2011 dialog receipt');

    const [nav] = await Promise.all([
      page.waitForEvent('framenavigated', { timeout: 1500 }).catch(() => null),
      page.locator('#stock-form button[type=submit]').click(),
      expect(page.locator('#result')).toContainText('Stock movement created'),
    ]);
    expect(nav, 'submitting must not trigger a full page navigation').toBeNull();

    // The table refreshed itself in place via the existing stock-updated
    // HX-Trigger, and the dialog is deliberately left open (closing on
    // success would hide the confirmation the merchant just saw).
    await expect(page.locator('.stock-row', { hasText: 'Pepsi Can 330ml' }).first().locator('td').nth(3))
      .toHaveText(String(startingQty + 3));
    await expect(page.locator(DIALOG)).toBeVisible();

    // ...but the result line must NOT survive into the NEXT open. Found in
    // review: resetStockForm() cleared the form's fields (form.reset()) and
    // #result is not a field, so closing after a save and then tapping a
    // DIFFERENT item reopened the dialog with "Stock movement created: …"
    // from the previous item still under the blank form — reading as if the
    // new item's adjustment had already been applied.
    await page.locator('#stock-dialog-close').click();
    await expect(page.locator(DIALOG)).toBeHidden();
    const other = page.locator('#stock-table .stock-row')
      .filter({ hasNotText: 'Pepsi Can 330ml' }).first();
    await other.locator('td').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#result')).toHaveText('');
    assertClean();
  });

  // The low-stock card is a SEPARATE, hand-built HTML fragment swapped in by
  // htmx (GET /api/inventory/low-stock), and re-swapped on every
  // stock-updated event. Since this card its rows carry the same .stock-row
  // class, so the ONE delegated listener is meant to cover them too — and
  // the page's search filter had to be re-scoped to #stock-table so it
  // stops short of them.
  //
  // Both of those are page-JS behaviours, and neither can be driven from the
  // demo till: `reorder_level` has no UI anywhere in the app (it arrives by
  // import/API only), so the seeded catalogue has no low-stock rows at all
  // and this card renders "No low stock items" — verified live. Rather than
  // add DB seeding machinery for one fragment, the fragment is reproduced in
  // place from a REAL stock row's own dataset, byte-for-byte in the shape
  // GetLowStock emits. That the server actually emits this shape is asserted
  // separately and directly, in Go, by
  // TestGetLowStock_RowsCarryDialogAttributes — so the two halves together
  // cover the path end to end without either one assuming the other.
  async function injectLowStockRow(page: import('@playwright/test').Page) {
    return page.evaluate(() => {
      const src = document.querySelector('#stock-table .stock-row') as HTMLElement;
      const d = src.dataset;
      const list = document.getElementById('low-stock-list')!;
      list.innerHTML =
        `<table class='table'><tbody><tr class='stock-row' data-item='${d.item}' ` +
        `data-name='${d.name}' data-sku='${d.sku}' data-location='${d.location}' ` +
        `data-location-name='${d.locationName}'><td>${d.name}</td><td>${d.sku}</td>` +
        `<td>${d.locationName}</td><td class='low-stock'>1.00</td><td>10</td></tr></tbody></table>`;
      return { name: d.name, locationName: d.locationName };
    });
  }

  test('(f) a low-stock row, in a fragment swapped in after load, opens the same dialog prefilled', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    const { name, locationName } = await injectLowStockRow(page);

    // Delegation is on `document`, so it must survive the container's
    // contents being replaced wholesale — the thing that silently breaks the
    // day someone "tidies" the listener onto #stock-table.
    await page.locator('#low-stock-list .stock-row td').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#stock-item-id')).not.toHaveValue('');
    await expect(page.locator('#stock-item-search')).toHaveValue(new RegExp(name!));
    const ctx = page.locator('#stock-dialog-context');
    await expect(ctx).toContainText(name!);
    await expect(ctx).toContainText(locationName!);
    assertClean();
  });

  test('(g) the stock search filter never hides low-stock rows', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/inventory');
    await injectLowStockRow(page);

    await page.locator('#stock-search').fill('zzz-matches-nothing');
    await expect(page.locator('#stock-table .stock-row:not([hidden])')).toHaveCount(0);
    // Same name as a now-hidden #stock-table row, and still visible: the
    // filter is scoped, so it cannot desync the low-stock card from its own
    // "N running out" badge.
    await expect(page.locator('#low-stock-list .stock-row')).toBeVisible();
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600', oneRow: true },
    { width: 360, height: 740, label: 'phone 360px', oneRow: false },
  ]) {
    test(`(e) at ${vp.label} the dialog is full-bleed and the head stays pinned to one row`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/inventory');
      await page.locator('#stock-dialog-open').click();

      const dlg = page.locator(DIALOG);
      await expect(dlg).toBeVisible();
      const box = (await dlg.boundingBox())!;
      expect(box.x).toBe(0);
      expect(box.y).toBe(0);
      expect(Math.round(box.width)).toBe(vp.width);

      const head = (await page.locator(`${DIALOG} .record-dialog-head`).boundingBox())!;
      // ut-docs#2000's finding was a dialog head eating 42% of a 360px
      // screen -- this head carries only a title and a close button.
      expect(head.height / vp.height).toBeLessThan(0.3);

      const closeBtn = (await page.locator('#stock-dialog-close').boundingBox())!;
      expect(closeBtn.x + closeBtn.width).toBeLessThanOrEqual(vp.width + 0.5);
      expect(closeBtn.y).toBeGreaterThanOrEqual(0);
      if (vp.oneRow) {
        // Title and close button share the head's one row -- the row is
        // align-items:center, so their vertical CENTERS align even when
        // their heights differ (an h3's text line vs. an icon button),
        // same idea as ut-docs#2010's own "head controls share one row"
        // assertion.
        const title = (await page.locator('#stock-dialog-title').boundingBox())!;
        expect(Math.round(title.y + title.height / 2)).toBe(Math.round(closeBtn.y + closeBtn.height / 2));
      }
      // Scoped to the dialog itself, not document.documentElement:
      // #stock-table (the page underneath, still in the DOM behind the
      // fixed-position dialog) has its own pre-existing horizontal overflow
      // at 360px, unrelated to this card -- confirmed by reproducing it on
      // a fresh /inventory load with the dialog never opened at all; filed
      // separately rather than silently widening this card's scope to fix
      // it. What THIS card owns is the dialog's own content fitting.
      expect(await page.locator(`${DIALOG} .record-dialog-body`).evaluate(
        (el) => el.scrollWidth - el.clientWidth,
      )).toBeLessThanOrEqual(0);
      assertClean();
    });
  }
});
