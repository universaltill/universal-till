import { test, expect } from './fixtures';
import { watchConsole, setOskMode, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#1284: found by independent review of ut-docs#1275, out of that
// card's explicitly-scoped five admin-screen files. Same root cause as
// ut-docs#1249/#1272/#1275 (osk.js's insert() does a naive `value += text`
// for type="number" fields, and a number input silently resets .value to ""
// on any momentarily-invalid decimal string mid-type — "2.50" ends up "50",
// not empty). This card's fields are on the highest-OSK-exposure surface in
// the product (the sale screen, reachable on every till on every sale) plus
// the catalog/variant price fields ut-docs#1275's own repo-wide grep missed.
//
// None of these fields copy into a separate hidden field via an onchange
// handler that osk.js's input-only keystrokes never fire (verified during
// diagnosis: index.html's amount/change are read from FormData by
// addPayment(), item-price/variant-price/variant-cost are read from .value
// at submit time by an existing submit/htmx:configRequest handler, and
// cost/priceDeltaMajor are parsed server-side from the raw posted string) —
// so, unlike ut-docs#1249's #pfand-amount, the fix here is the
// type="text" inputmode="decimal" + pattern swap alone, no oninput handler.
//
// Drives osk.js's real on-screen keys (button[data-k]), NOT
// page.fill()/type(), which bypass osk.js's insert() entirely and would
// pass even on the unfixed type="number" markup.
async function typeViaOsk(page: import('@playwright/test').Page, text: string) {
  for (const ch of text) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

// ut-docs#1385: #payment-overlay opens via showModal() (a genuine native
// modal dialog), which makes the whole document outside it -- #osk
// included, since it's a single instance appended to <body>, never
// re-parented into whichever dialog is open -- inert per the HTML living
// standard: unfocusable and excluded from hit-testing. #osk still renders
// (confirmed live: getBoundingClientRect()/getComputedStyle both show it
// correctly laid out and visible), but a real tap on it, or a Playwright
// click(), never reaches the button -- reproducibly intercepted by the
// dialog. Every OTHER OSK-hosting dialog in this codebase (#hold-modal/
// #pfand-modal/#elevation-modal/#table-add-modal) is deliberately opened
// non-modal (.show(), not .showModal()) for exactly this reason (see the
// block comment above #hold-modal in app.css) -- #payment-overlay was
// simply never given the same treatment. That is a separate, more severe
// bug (the OSK cannot be opened AT ALL for these fields on real kiosk
// hardware, independent of what type= they use) than this card's
// decimal-corruption scope, so it is filed separately (ut-docs#1385)
// rather than folded into this fix.
//
// Until #1385 lands, a real click-driven reproduction of split-tender's
// amount/change fields is not possible even with the fix in place. This
// mirrors osk.js's insert() function body verbatim (the exact mechanism
// under test: setRangeText for a cursor-aware type, naive `value += text`
// for type="number"/"email", which don't expose a selection) directly
// against the field, which is the necessary and sufficient condition the
// real button press would exercise if #1385's dialog fix already existed --
// it is not a reinterpretation of the bug, it IS the bug's own mechanism.
async function simulateOskInsertViaField(page: import('@playwright/test').Page, selector: string, text: string) {
  const field = page.locator(selector);
  for (const ch of text) {
    await field.evaluate((el: HTMLInputElement, character: string) => {
      if (typeof el.setRangeText === 'function' && el.type !== 'number' && el.type !== 'email') {
        el.setRangeText(character, el.selectionStart ?? el.value.length, el.selectionEnd ?? el.value.length, 'end');
      } else {
        el.value += character;
      }
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, ch);
  }
}

test.describe('sale-screen and catalog/variant decimal fields survive typing via the on-screen keyboard (ut-docs#1284)', () => {
  test.afterEach(async ({ page }) => {
    await setOskMode(page, 'auto');
    await page.request.post('/api/pos/reset').catch(() => {});
  });

  test('split-tender amount: typing "2.50" via the OSK produces exactly "2.50", not a corrupted value', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.locator('.scan-row input[name="code"]').fill('5000000000029'); // Pepsi Can 330ml
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();

    const amount = page.locator('#split-tender-form input[name="amount"]');
    await amount.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    // See simulateOskInsertViaField's own comment (ut-docs#1385) for why
    // this isn't a real click sequence.
    await simulateOskInsertViaField(page, '#split-tender-form input[name="amount"]', '2.50');
    await expect(amount).toHaveValue('2.50');

    assertClean();
  });

  test('split-tender change: typing "0.75" via the OSK produces exactly "0.75", not a corrupted value', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.locator('.scan-row input[name="code"]').fill('5000000000029');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    await page.getByTestId('payment-open').click();
    await page.locator('.tender .tab', { hasText: /split/i }).click();

    const change = page.locator('#split-tender-form input[name="change"]');
    await change.click();
    await change.fill(''); // starts pre-filled ("0.00") -- clear before typing
    await change.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    // See simulateOskInsertViaField's own comment (ut-docs#1385) for why
    // this isn't a real click sequence.
    await simulateOskInsertViaField(page, '#split-tender-form input[name="change"]', '0.75');
    await expect(change).toHaveValue('0.75');

    assertClean();
  });

  // Bananas (SKU-0026) is the demo catalogue's weighed item -- the only kind
  // of basket line whose qty-input ever takes a decimal (step="0.01" only
  // when IsWeighed; the pattern fix must keep that split, not just swap the
  // type for every line indiscriminately).
  test('basket qty-input on a weighed line: typing "2.50" via the OSK produces exactly "2.50"', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.locator('.scan-row input[name="code"]').fill('5000000000265'); // Bananas, weighed
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await page.waitForSelector('.basket table tbody tr');

    const qty = page.locator('.basket .qty-input').first();
    await qty.click();
    await qty.fill('');
    await qty.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '2.50');
    await expect(qty).toHaveValue('2.50');

    assertClean();
  });

  test('catalog.html item-price: typing "9.99" via the OSK produces exactly "9.99"', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/catalog');
    await openNewItemForm(page);

    const price = page.locator('#item-price');
    await price.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '9.99');
    await expect(price).toHaveValue('9.99');

    assertClean();
  });

  // Shared setup for the catalog_variants.html tests below: a fresh probe
  // item with its variants/modifiers panel open. Guards the item-save
  // request explicitly (review finding, ut-docs#1284: a bare `expect(...)
  // .toBeVisible()` retry loop with no waitForResponse was the one
  // observed flake source in this file, unlike every scan elsewhere in
  // this suite which already waits on its own request).
  // Returns the new item's id (ut-docs#2330: two callers below need it to
  // seed a modifier group via the API now that neither the item-editor's
  // own dialog nor /modifiers can create one from a standing start — see
  // their own comments) -- read from the row's data-id BEFORE it disappears
  // under the reopened dialog, rather than assuming it is still queryable
  // once .catalog-row is (maybe) covered.
  async function createProbeItemAndOpenVariants(page: import('@playwright/test').Page, name: string): Promise<string> {
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    await page.locator('#item-price').fill('1.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();

    // ut-docs#1956: the dialog is FULL-screen now, so the row underneath
    // cannot be clicked until the create dialog is gone — close it
    // explicitly (closeItemForm, ut-docs#1929: race-tolerant of the
    // save-success auto-close timer) rather than waiting on that timer.
    await closeItemForm(page);
    const row = page.locator('.catalog-row', { hasText: name });
    const itemId = await row.getAttribute('data-id');
    await row.click();
    // …and the variants panel is the reopened dialog's Variants tab, not a
    // page-level section any more — so the dialog STAYS open here.
    await page.locator('#item-form-tab-variants').click();
    await expect(page.locator('#catalog-variants')).toBeVisible();
    return itemId!;
  }

  // The field ut-docs#1284's own issue body actually names at this line
  // (~37) -- the item's own cost price, on the always-present item-cost
  // form.
  test('catalog_variants.html item cost field survives OSK typing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/catalog');
    await createProbeItemAndOpenVariants(page, 'OSK Cost Probe ' + Date.now());

    const cost = page.locator('#catalog-variants form[hx-post="/api/catalog/item-cost"] input[name="cost"]');
    await cost.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '4.20');
    await expect(cost).toHaveValue('4.20');

    assertClean();
  });

  // Review finding (ut-docs#1284): the first draft of this file only
  // exercised the "add variant" (vf-new) row's price/cost fields, which
  // the issue never named -- the fields it DID name (lines ~107/108) are
  // the EXISTING-variant row's own price/cost, a structurally different
  // element (form="vf-{{.ID}}" vs form="vf-new", data-minor-seeded vs
  // blank). Create one real variant first so an existing row exists, then
  // target ITS fields specifically (`.vg-row:not(.vg-new)` excludes the
  // still-present "add variant" row).
  test('catalog_variants.html EXISTING variant row price/cost survive OSK typing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/catalog');
    await createProbeItemAndOpenVariants(page, 'OSK Existing Variant Probe ' + Date.now());

    await page.locator('input[form="vf-new"][name="name"]').fill('330ml');
    await page.locator('input[form="vf-new"].variant-price-major').fill('2.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
      page.locator('button[form="vf-new"][type=submit]').click(),
    ]);
    const existingRow = page.locator('.vg-row:not(.vg-new)');
    await expect(existingRow).toBeVisible();

    // Both fields are pre-filled from the just-saved variant's own
    // data-minor (the panel's own inline <script> renders it back as
    // major units on load) -- clear before typing, or osk.js's insert()
    // appends onto the existing value instead of replacing it.
    const variantPrice = existingRow.locator('.variant-price-major');
    await variantPrice.click();
    await variantPrice.fill('');
    await variantPrice.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '3.30');
    await expect(variantPrice).toHaveValue('3.30');

    const variantCost = existingRow.locator('.variant-cost-major');
    await variantCost.click();
    await variantCost.fill('');
    await variantCost.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '1.65');
    await expect(variantCost).toHaveValue('1.65');

    assertClean();
  });

  // Review finding (ut-docs#1284): same gap as the variant row above --
  // the issue names the EXISTING option's price-delta (line ~183), not
  // the "add option" row's (line ~195). Create a group, then a real
  // option inside it, then target that option's own field
  // (`:has(input[name="id"])` is what distinguishes an existing option
  // form from the group's still-present "add option" form, which has no
  // hidden id field).
  test('modifiers.html EXISTING modifier-option price-delta survives OSK typing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');

    // ut-docs#2330: the item-editor's own "Manage Modifiers" dialog is now
    // attach/detach-only -- no group/option create/edit reachable from
    // there any more (that stays exclusively on /modifiers, the shop-wide
    // full-CRUD home for modifier groups since ut-docs#1957). /modifiers
    // itself only lists items that ALREADY own a group (it folds
    // ListAllShopModifierGroups' flat, shop-wide slice by item -- see
    // handlers.go's groupModifierAdminByItem), so it can't bootstrap a
    // shop's very first group either -- there is no "add a new group" form
    // anywhere until at least one exists. Seed the group + a real option
    // through the same API the UI forms themselves post to (the pattern
    // catalog-item-editor-attach-only-2330.spec.ts's own createItem/group
    // seeding already established), so this item's card appears on
    // /modifiers with full CRUD. This test's actual subject -- the
    // priceDeltaMajor OSK-decimal bug -- lives entirely in the real
    // rendered <input> the OSK types into below; seeding the scaffolding
    // that gets us there doesn't touch it.
    const itemName = 'OSK Existing Option Probe ' + Date.now();
    const createResp = await page.request.post('/api/catalog/item', { form: { name: itemName, price: '100' } });
    expect(createResp.ok(), 'create item').toBe(true);
    await page.goto('/catalog');
    const itemId = await page.locator(`.catalog-row[data-name="${itemName}"]`).getAttribute('data-id');
    expect(itemId, 'item must have a data-id').not.toBeNull();

    const groupName = 'Milk ' + Date.now();
    const groupResp = await page.request.post('/api/catalog/modifier-group', {
      form: { itemId: itemId!, name: groupName, minSelect: '0', maxSelect: '1' },
    });
    expect(groupResp.ok(), 'create modifier group').toBe(true);

    // The group-create POST doesn't hand back the new group's id (it
    // answers with an HTML fragment, not JSON) -- read it back from the
    // modifier-groups-panel fragment instead, same read-back trick
    // sell-screen-categories-tab-2283.spec.ts's seedModifier uses.
    const panelResp = await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`);
    const panelHtml = await panelResp.text();
    const nameIdx = panelHtml.indexOf(`>${groupName}<`);
    expect(nameIdx, 'panel must contain the new group').toBeGreaterThan(-1);
    const idMatches = [...panelHtml.slice(0, nameIdx).matchAll(/data-group-id="([^"]*)"/g)];
    expect(idMatches.length).toBeGreaterThan(0);
    const groupId = idMatches[idMatches.length - 1][1];

    const optionResp = await page.request.post('/api/catalog/modifier-option', {
      form: { groupId, itemId: itemId!, name: 'Oat milk' },
    });
    expect(optionResp.ok(), 'create modifier option').toBe(true);

    await page.goto('/modifiers');
    const group = page.locator('.modifier-admin-group').filter({ has: page.locator(`input[name="name"][value="${groupName}"]`) });
    await expect(group).toBeVisible();
    const existingOption = group.locator('form.modifier-admin-option-row:has(input[name="id"])');
    await expect(existingOption).toBeVisible();

    const priceDelta = existingOption.locator('input[name="priceDeltaMajor"]');
    await priceDelta.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '0.50');
    await expect(priceDelta).toHaveValue('0.50');

    assertClean();
  });

  // The "add variant"/"add option" rows themselves -- same bug class,
  // adjacent duplicates of the fields covered above, found via the same
  // repo-wide diagnosis but not individually named in the issue body.
  test('catalog_variants.html new-variant row and new-modifier-option row survive OSK typing', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/catalog');
    const itemId = await createProbeItemAndOpenVariants(page, 'OSK New-Row Probe ' + Date.now());

    const variantPrice = page.locator('input[form="vf-new"].variant-price-major');
    await variantPrice.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '3.30');
    await expect(variantPrice).toHaveValue('3.30');

    const variantCost = page.locator('input[form="vf-new"].variant-cost-major');
    await variantCost.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '1.65');
    await expect(variantCost).toHaveValue('1.65');

    // ut-docs#2330: the item-editor's own "Manage Modifiers" dialog lost its
    // "add group"/"add option" forms (attach/detach-only now), and
    // /modifiers can't bootstrap this item's first group either -- it only
    // lists items that already own one (see the sibling test above's own
    // note). Seed the group itself via the same API the UI form posts to,
    // leaving it with zero options, so /modifiers renders this item's card
    // with its "add option" row still a real, un-submitted <form> --
    // that row's own priceDeltaMajor field is this test's actual subject.
    const groupName = 'OSK New-Row Modifier ' + Date.now();
    const groupResp = await page.request.post('/api/catalog/modifier-group', {
      form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
    });
    expect(groupResp.ok(), 'create modifier group').toBe(true);

    await page.goto('/modifiers');
    const group = page.locator('.modifier-admin-group').filter({ has: page.locator(`input[name="name"][value="${groupName}"]`) });
    await expect(group).toBeVisible();

    const priceDelta = group.locator('input[name="priceDeltaMajor"]');
    await priceDelta.click();
    await expect(page.locator('#osk.osk-open')).toBeVisible();
    await typeViaOsk(page, '0.50');
    await expect(priceDelta).toHaveValue('0.50');

    assertClean();
  });
});
