import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { drainParkedOrders, watchConsole } from './helpers';

// ut-docs#2147: split out of the ut-docs#2138 review (universaltill/
// universal-till#1103's code-review record, finding N3 + "process
// observations"). ut-docs#2138 fixed four real, measured CSS layout bugs
// on /open-orders' row-as-one-button pattern -- header/value misalignment
// at every viewport, ~25% of each row untappable, a 360px-floor width
// regression, and (found at close-out) a long label collapsing to nothing
// at 360px -- and every one of them was caught only by eyeballing a real
// browser, none by an automated test. This file is that regression test,
// modelled on categories-record-dialog-2010.spec.ts's own real-geometry
// assertions (never scrollWidth === clientWidth -- a hidden-overflow box
// always reports those equal, the false pass that spec's own header notes).
//
// It also covers the OTHER half of ut-docs#2147: the row buttons' (and the
// sale-screen popup's own parked-order buttons') accessible name used to be
// just their visible spans concatenated, with no statement of what
// activating the control does. Both surfaces now prepend a visually-hidden
// "Resume order" span -- open_orders.html / parked_orders.html carry the
// full reasoning in their own comments.

const COLUMNS = ['label', 'table', 'lines', 'total', 'age'] as const;

async function parkASale(page: Page, label: string) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

test.describe('/open-orders row geometry + accessible name (ut-docs#2147)', () => {
  test.afterEach(async ({ page }) => {
    await drainParkedOrders(page.request);
  });

  for (const vp of [
    { width: 360, height: 740, label: 'phone 360px' },
    { width: 1024, height: 600, label: 'kiosk floor 1024x600' },
    { width: 1280, height: 800, label: 'pilot tablet 1280x800' },
  ]) {
    test(`(e) at ${vp.label} every header lines up with its own column's value`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/');
      await parkASale(page, 'Geometry Probe ' + Date.now());
      await page.goto('/open-orders');

      const row = page.locator('#open-orders-table tbody .open-order-row-btn').first();
      await expect(row).toBeVisible();

      // The whole point of the ut-docs#2138 fix: the header is laid out by
      // the SAME flex box as the row values (a <th> can't take `flex` while
      // its <tr> is still display:table-row), so before that fix the two
      // disagreed by 100-356px at every viewport measured. Assert real
      // pixel alignment per column, not just "both exist".
      for (const col of COLUMNS) {
        const th = (await page.locator(`#open-orders-table thead .open-order-cell-${col}`).boundingBox())!;
        const val = (await row.locator(`.open-order-cell-${col}`).boundingBox())!;
        expect(Math.abs(th.x - val.x), `${col} column: header.x=${th.x} value.x=${val.x}`).toBeLessThanOrEqual(1);
      }

      // The row fills the list, not ~75% of it (the other measured bug --
      // .users-list .table's display:block escape hatch shrink-to-fit the
      // table box, leaving roughly a quarter of every row untappable).
      const rowBox = (await row.boundingBox())!;
      const tableBox = (await page.locator('#open-orders-table').boundingBox())!;
      expect(rowBox.width / tableBox.width).toBeGreaterThan(0.95);

      // No page-level horizontal overflow introduced by this list at the
      // documented floors.
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth))
        .toBeLessThanOrEqual(0);
      assertClean();
    });
  }

  // ut-docs#2138's own fourth bug, found only at close-out: a long
  // cashier-typed label (the one field with no min-inline-size floor at
  // the time) shrank to 0 and vanished under the phone-floor's combined
  // column pressure, rather than truncating like a bounded field would.
  // maxHoldLabelRunes (hold_api.go) is 64 -- this drives a label past that
  // to also confirm server-side truncation still leaves a real, non-empty,
  // still-long string to lay out.
  test('a long cashier-typed label keeps its own floor width instead of collapsing to zero at 360px', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/');
    const longLabel = 'Table for the whole regulars party, extra napkins, split the bill four ways please';
    await parkASale(page, longLabel);
    await page.goto('/open-orders');

    const row = page.locator('#open-orders-table tbody .open-order-row-btn').first();
    await expect(row).toBeVisible();

    // Each column's own min-inline-size floor (app.css: label/lines 3rem,
    // total 5rem, age 6rem) at this suite's 17px root font -- pinned to a
    // real pixel bound, not just ">0" (review finding: a regression to
    // HALF the floor would still pass a bare ">0" check, and a bare
    // toBeVisible() passes for a 0-width element with non-zero height).
    // A little headroom below the exact measured value (51/51/85/102px)
    // so this isn't brittle to sub-pixel font-rendering differences.
    const FLOORS = { label: 40, lines: 40, total: 70, age: 85 } as const;
    for (const col of Object.keys(FLOORS) as Array<keyof typeof FLOORS>) {
      const cell = row.locator(`.open-order-cell-${col}`);
      await expect(cell).not.toHaveText('');
      const box = (await cell.boundingBox())!;
      expect(box.width, `${col} column collapsed under label pressure (floor ${FLOORS[col]}px)`)
        .toBeGreaterThan(FLOORS[col]);
    }
    assertClean();
  });

  // The other half of ut-docs#2147: the accessible name now states the
  // action, on both surfaces that render this pattern. getByRole resolves
  // through the browser's own accessible-name computation (text-node
  // concatenation, since neither button sets aria-label) -- a stronger
  // check than reading an attribute, since it's the real thing a screen
  // reader announces.
  test('the /open-orders row and the sale-screen popup row both announce "Resume order" plus the existing detail', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const label = 'Accessible Name Probe ' + Date.now();
    await parkASale(page, label);

    await page.goto('/open-orders');
    const pageRow = page.getByRole('button', { name: new RegExp(`Resume order.*${label}`) });
    await expect(pageRow).toBeVisible();
    // The pre-existing detail (money total, age) must still be part of the
    // announced name -- this fix must not have replaced it.
    await expect(pageRow).toHaveAccessibleName(/£\d/);

    await page.goto('/');
    await page.locator('.tender-quickpay [data-testid="parked-orders-open"]').click();
    const modal = page.locator('#parked-orders-modal');
    await expect(modal).toBeVisible();
    const popupRow = modal.getByRole('button', { name: new RegExp(`Resume order.*${label}`) });
    await expect(popupRow).toBeVisible();
    assertClean();
  });
});
