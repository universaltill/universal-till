import { test, expect } from '@playwright/test';
import { setOskMode } from './helpers';

// ut-docs#1275: found by independent review of ut-docs#1272's fix, out of
// that card's scope. Same root cause as ut-docs#1249/#1272 (osk.js's
// insert() does a naive `value += text` for type="number" fields, and a
// number input silently resets .value to "" on any momentarily-invalid
// decimal string while typing) -- but on fields that submit type="number"
// directly via name= (no separate hidden field), so the #1249-class
// "always empty" bug doesn't apply here. The failure mode is worse: it
// silently succeeds with the WRONG value (verified in #1249: typing
// '2','.','5','0' produced a final .value of "50", not "2.50" -- a 20×
// wrong value), not a visibly empty/dead field.
//
// Fix: type="text" inputmode="decimal" + a numeric pattern, same
// text+inputmode="decimal" pattern as #pfand-amount (ut-docs#1249) and the
// five shifts/tips fields (ut-docs#1272) -- unlike those, none of these
// fields copy into a separate hidden field via an onchange handler that
// osk.js's input-only keystrokes never fire, so no oninput handler is
// needed here; the fix is the type/inputmode/pattern swap alone. (The one
// exception, inventory.html's #stock-cost, DOES have a hidden companion
// field, #stock-cost-minor -- but it's read on the form's submit event,
// not onchange, so it already picks up whatever osk.js last typed; only
// the decimal-corruption bug applied there, covered below.)
//
// Drives osk.js's real on-screen keys (button[data-k]), NOT
// page.fill()/type(), which bypass osk.js's insert() entirely and would
// pass even on the unfixed type="number" markup.
async function typeViaOsk(page: import('@playwright/test').Page, text: string) {
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

test.describe('admin-screen decimal fields survive typing via the on-screen keyboard (ut-docs#1275)', () => {
  test.afterEach(async ({ page }) => {
    // OSK mode is a server-side setting shared by every spec on this
    // server -- restore it even when the test body fails (helpers.ts's own
    // documented convention for setOskMode).
    await setOskMode(page, 'auto');
  });

  const cases: { name: string; goto: string; before?: (page: import('@playwright/test').Page) => Promise<void>; selector: string; text: string }[] = [
    {
      name: 'promotions.html value_amount (new-promotion form)',
      goto: '/promotions',
      selector: '.users-form input[name="value_amount"]',
      text: '2.50',
    },
    {
      name: 'promotions.html value_percent (new-promotion form)',
      goto: '/promotions',
      before: async (page) => {
        await page.locator('.users-form select[name="type"]').selectOption('percent');
      },
      selector: '.users-form input[name="value_percent"]',
      text: '17.50',
    },
    {
      // .fee-row repeats per payment method (Cash/Card/Gift Card on the
      // seeded e2e till) -- the loop below always takes .first().
      name: 'settings.html Payments fee percent',
      goto: '/settings',
      selector: '.fee-row input[name="percent"]',
      text: '12.50',
    },
    {
      name: 'settings.html Payments fee fixed',
      goto: '/settings',
      selector: '.fee-row input[name="fixed"]',
      text: '3.40',
    },
    {
      name: 'inventory.html quantity',
      goto: '/inventory',
      selector: '#stock-form input[name="quantity"]',
      text: '2.50',
    },
    {
      name: 'inventory.html #stock-cost',
      goto: '/inventory',
      selector: '#stock-cost',
      text: '4.20',
    },
    {
      name: 'tax_codes.html rate',
      goto: '/catalog/tax-codes',
      selector: '#tax-code-rate',
      text: '7.50',
    },
    {
      name: 'tax_codes.html takeawayRate',
      goto: '/catalog/tax-codes',
      selector: '#tax-code-takeaway-rate',
      text: '5.25',
    },
    {
      name: 'country_settings.html tax_rate_pct (new-country form)',
      goto: '/country-settings',
      selector: '.users-form input[name="tax_rate_pct"]',
      text: '8.25',
    },
  ];

  for (const c of cases) {
    test(`${c.name}: typing "${c.text}" via the OSK produces exactly "${c.text}", not a corrupted value`, async ({ page }) => {
      await setOskMode(page, 'on');
      await page.goto(c.goto);
      if (c.before) await c.before(page);

      const field = page.locator(c.selector).first();
      await expect(field, `${c.name} must be present and visible`).toBeVisible();
      await field.click();
      // country_settings.html's new-country tax_rate_pct (and possibly
      // others) ships pre-filled with "0" -- clear it first so this test
      // exercises "does a typed decimal corrupt", not "does osk.js append
      // to a pre-filled value", which isn't what this card is about.
      await field.fill('');
      await field.click();
      await expect(page.locator('#osk.osk-open')).toBeVisible();
      await typeViaOsk(page, c.text);

      // The bug under test: on the unfixed type="number" markup, typing the
      // decimal point produces a momentarily-invalid float string and the
      // browser resets .value to "" right then -- so the final value ends
      // up wrong (e.g. "250" from "2.50"), not simply absent.
      await expect(field).toHaveValue(c.text);
    });
  }

  // #stock-cost is the one field in this card with a hidden minor-units
  // companion (#stock-cost-minor, synced on the form's submit event) --
  // an end-to-end assertion through that sync is a materially stronger
  // check than the display-value-only one the loop above does for it,
  // matching how #1249/#1272's own specs asserted the POSTed field, not
  // just the visible one.
  test('inventory.html #stock-cost: a correctly-typed amount syncs to #stock-cost-minor on submit', async ({ page }) => {
    await setOskMode(page, 'on');
    await page.goto('/inventory');
    const cost = page.locator('#stock-cost');
    await cost.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '4.20');
    await expect(cost).toHaveValue('4.20');

    // The other required fields are unrelated to the bug under test --
    // filled plainly, real submit, same pattern inventory-to-till.spec.ts
    // already uses on this exact form.
    await page.locator('#stock-item-search').fill('Pepsi Can 330ml (SKU-0002)');
    await expect(page.locator('#stock-item-id')).not.toHaveValue('');
    await page.locator('#stock-location').selectOption({ label: 'Main Store' });
    await page.locator('#stock-form input[name="quantity"]').fill('1');
    await page.locator('#stock-form input[name="reason"]').fill('e2e: ut-docs#1275 stock-cost sync');

    // The sync handler runs synchronously in the 'submit' listener, so
    // #stock-cost-minor is already correct by the time click() resolves --
    // no need to wait for the (unrelated) htmx response first.
    await page.locator('#stock-form button[type=submit]').click();
    await expect(page.locator('#stock-cost-minor')).toHaveValue('420');
    await expect(page.locator('#result')).toContainText('Stock movement created');
  });

  // inventory.html's `quantity` field must keep accepting a leading '-'
  // (Quantity is "positive for receive, +/- for adjust",
  // internal/pages/inventory_api.go) -- the pattern fix must not narrow
  // this. osk.js's numeric layer has no '-' key at all (ut-docs#1276, a
  // separate open card), so this path is only reachable via a real/
  // external keyboard today -- driven directly here, not via the OSK.
  test('inventory.html quantity: a negative adjustment amount is still accepted (no OSK "-" key yet, ut-docs#1276)', async ({ page }) => {
    await page.goto('/inventory');
    const qty = page.locator('#stock-form input[name="quantity"]');
    await qty.fill('-5.25');
    await expect(qty).toHaveValue('-5.25');
    expect(await qty.evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(true);
  });
});
