import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { setOrderTypePromptMode } from './helpers';

// ut-docs#2282: a setting picks WHEN/WHERE the sale screen asks the cashier
// dine-in or takeaway -- top of basket (today's always-visible toggle, the
// default), before the first item lands in an empty basket, or deferred
// until Pay. ut-docs#2309 (product-owner scope change, outcome 1 taken):
// the per-line Dine-in/Takeaway control this card also removes is covered
// by this file's last test.
//
// ut-docs#2371: three bugs fixed in the "before_item" placement --
// (1) a modifier/variant tile's prompt only ever fired on the picker's OWN
// "Add to basket" submit, and opened UNDERNEATH #modifier-modal (both used
// non-modal .show()), unreachable; (2) it never fired at the START of a
// sale (New Sale, after a completed tender, page load with an empty
// unanswered basket) -- only lazily on the first add; (3) it wasn't a real
// modal at all -- no backdrop, fixed 8vh offset, the rest of the screen
// stayed tappable. Fixed: a real showModal() top-layer modal, gated BEFORE
// the modifier picker opens (not just on its own submit), and asked at
// sale start too (Cancel/Escape suppress just that sale-start nag for the
// rest of the sale, never the per-item/per-Pay gates).
//
// The setting is a SERVER-side value shared by every spec on this server
// (fixtures.ts's own "one live till, workers: 1" rule) -- every test here
// restores 'top' in afterEach even on failure, or a failed run leaks
// before_item/at_pay into an unrelated later spec's basket interactions.

// ut-docs#2371: minimal modifier-item seeding, adapted from
// sell-screen-categories-tab-2283.spec.ts's own makeItems/seedItems/
// seedModifier (not exported there -- that file's helpers build a whole
// multi-item/multi-category fixture set this file doesn't need; copied and
// trimmed down to exactly what the modifier-tile-gate test below needs:
// one item with one real modifier group/option, added as a sell-screen
// shortcut so ButtonStore.Load's HasModifiers flag is genuinely true and
// the tile really does hx-get="/ui/pos/modifiers...").
const MOD_RUN = Date.now().toString(36).toUpperCase();
type SeededModItem = { itemId: string; name: string; barcode: string; optionName: string };

async function seedModifierItem(page: Page): Promise<SeededModItem> {
  const name = `Otp2371 Mod ${MOD_RUN}`;
  const sku = `OTP2371M${MOD_RUN}`;
  const barcode = `OTP2371BC-M-${MOD_RUN}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},${sku},${barcode},1.00,Otp2371 Cat,1\n`;

  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-otp2371-${MOD_RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(csv),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);

  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${name}"]`);
  const itemId = (await row.first().getAttribute('data-id'))!;
  const addResp = await page.request.post('/api/buttons/add', {
    form: { itemId, label: name, code: barcode },
  });
  expect(addResp.ok(), 'add shortcut for modifier item').toBe(true);

  const groupName = `Size ${MOD_RUN}`;
  const groupResp = await page.request.post('/api/catalog/modifier-group', {
    form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' },
  });
  expect(groupResp.ok(), 'create modifier group').toBe(true);

  // Same read-back trick sell-screen-categories-tab-2283.spec.ts uses: the
  // group-create POST doesn't hand back the new group's id, so read the
  // modifier-groups-panel fragment and pull it from the stable
  // data-group-id attribute right before this group's own name text
  // (ut-docs#2330: that panel is attach/detach-only now, no id/name inputs
  // to scan for).
  const panelResp = await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`);
  expect(panelResp.ok(), 'fetch modifier-groups-panel').toBe(true);
  const html = await panelResp.text();
  const nameIdx = html.indexOf(`>${groupName}<`);
  expect(nameIdx, 'modifier-groups-panel must contain the new group').toBeGreaterThan(-1);
  const idMatches = [...html.slice(0, nameIdx).matchAll(/data-group-id="([^"]*)"/g)];
  expect(idMatches.length, 'modifier-groups-panel must expose the new group id').toBeGreaterThan(0);
  const groupId = idMatches[idMatches.length - 1][1];

  const optionName = `Large ${MOD_RUN}`;
  const optResp = await page.request.post('/api/catalog/modifier-option', {
    form: { groupId, itemId, name: optionName },
  });
  expect(optResp.ok(), 'create modifier option').toBe(true);

  return { itemId, name, barcode, optionName };
}

async function cleanupModifierItem(page: Page, seeded: SeededModItem | null): Promise<void> {
  if (!seeded) return;
  await page.request.post('/api/buttons/remove', { form: { code: seeded.barcode } });
  await page.goto('/catalog');
  const row = page.locator(`.catalog-row[data-name="${seeded.name}"]`);
  if ((await row.count()) === 0) return;
  const id = await row.first().getAttribute('data-id');
  if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
}

test.describe('ut-docs#2282 dine-in/takeaway prompt placement', () => {
  // Tracks whether this file's modifier-item test seeded a fixture item,
  // so afterEach can clean it up even if the test itself failed partway.
  let seededModItem: SeededModItem | null = null;

  test.afterEach(async ({ page }) => {
    await setOrderTypePromptMode(page, 'top');
    await page.request.post('/api/pos/reset').catch(() => {});
    await cleanupModifierItem(page, seededModItem);
    seededModItem = null;
  });

  test('top (default): the basket-top toggle is visible immediately and no intercept modal exists in the flow', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('[data-testid="order-type-dine-in"]')).toBeVisible();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toBeVisible();

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    // The modal must never have been shown (it isn't even open, and no
    // scan was intercepted -- the item landed straight away).
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
  });

  test('before_item: the very first item into an empty basket is intercepted; answering adds it and lifts the gate for the rest of the sale', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    // ut-docs#2371: the sell screen now asks at sale start too -- Cancel
    // it first so the gate under test here is the item-add one, not the
    // load-time one (see this file's dedicated "New Sale re-asks" and
    // "a modifier tile is intercepted" tests for the sale-start/modifier-
    // picker gates specifically).
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();

    // Intercepted: the modal opens and the item has NOT landed yet.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

    // Answer Takeaway -- the item lands, tagged takeaway (the whole-basket
    // toggle reflects the choice).
    await page.getByTestId('order-type-prompt-takeaway').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveClass(/is-active/);

    // A second item on the same (now non-empty, already-answered) basket
    // is NOT intercepted again.
    await page.locator('.scan-row input[name="code"]').fill('5000000000029');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toContainText('Pepsi');
  });

  test('before_item: Cancel closes the modal and leaves the item unadded', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    // ut-docs#2371: dismiss the load-time prompt first (see the comment
    // in the test above) so this test exercises the item-add gate, same
    // as it always did.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();

    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
  });

  test('at_pay: items add freely with no prompt; the Pay button is intercepted until answered, then opens the payment overlay', async ({ page }) => {
    await setOrderTypePromptMode(page, 'at_pay');
    await page.goto('/');

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    // No prompt on the item add itself under this placement.
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.getByTestId('payment-open').click();
    // Intercepted: the modal opens and the payment overlay does NOT.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#payment-overlay')).not.toBeVisible();

    await page.getByTestId('order-type-prompt-dine-in').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    // Answered -- the overlay now opens.
    await expect(page.locator('#payment-overlay')).toBeVisible();
  });

  // ut-docs#2371: the placement's `top`/`at_pay` behaviour is unchanged --
  // only the dialog itself is now a real modal wherever it opens. This is
  // the direct, DOM-level pin for that, same `:modal` pseudo-class other
  // specs (categories-record-dialog-2010.spec.ts, items-shell-catalog-
  // import-taxcodes-dialog-2095.spec.ts) already use to tell a real
  // showModal() dialog apart from a merely-`[open]` non-modal one.
  test('before_item: the prompt opens as a centred modal with a backdrop when the sell screen loads with an empty basket', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    // Visible with NO tap at all -- ut-docs#2371 bug #2 (it used to only
    // ever fire lazily on the first add).
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    // A real showModal() top-layer modal, not just an `[open]` dialog.
    await expect(page.locator('#order-type-prompt-modal:modal')).toHaveCount(1);

    const modalBox = (await page.locator('#order-type-prompt-modal').boundingBox())!;
    const viewport = page.viewportSize()!;
    const modalCenterX = modalBox.x + modalBox.width / 2;
    const modalCenterY = modalBox.y + modalBox.height / 2;
    expect(Math.abs(modalCenterX - viewport.width / 2)).toBeLessThanOrEqual(8);
    expect(modalCenterY).toBeGreaterThan(viewport.height / 3);
    expect(modalCenterY).toBeLessThan((viewport.height * 2) / 3);

    // ut-docs#2371 bug #1/#3: a real modal makes the rest of the document
    // inert -- a tap at a product tile's own on-screen position must not
    // reach it (the OLD non-modal .show() left the screen behind it fully
    // tappable, "locking the popup" by letting a tile add underneath it).
    const tile = page.locator('.btn-tile').first();
    await expect(tile).toBeVisible();
    const tileBox = (await tile.boundingBox())!;
    let scanRequestSeen = false;
    page.on('request', (req) => {
      if (req.url().includes('/api/pos/scan')) scanRequestSeen = true;
    });
    await page.mouse.click(tileBox.x + tileBox.width / 2, tileBox.y + tileBox.height / 2);
    await page.waitForTimeout(300);
    expect(scanRequestSeen, 'a tile behind the modal must not have been reachable').toBe(false);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
  });

  // ut-docs#2371 bug #2: New Sale re-asks even though this same tab
  // already answered once earlier in the session -- a fresh sale gets a
  // fresh question, with no tile tap needed to trigger it.
  test('before_item: New Sale re-asks', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await page.getByTestId('order-type-prompt-dine-in').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    await page.getByTestId('kiosk-checkout-start').click();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#order-type-prompt-modal:modal')).toHaveCount(1);
  });

  // ut-docs#2371 bug #1: a modifier/variant tile's OWN picker-opening
  // hx-get is gated BEFORE the picker ever appears -- not just on the
  // picker's later "Add to basket" submit, and the gate opens ABOVE
  // #modifier-modal rather than underneath it.
  // ut-docs#2371 (Tester finding): the sell screen's #basket is NOT in the
  // initial HTML -- index.html ships a placeholder <div class="basket"
  // hx-get="/ui/basket" hx-trigger="load"> that only becomes #basket once
  // that load swap lands. A sale-start check that runs before then sees
  // "no basket" and would misread it as "empty, unanswered" -- and once the
  // real basket arrives saying the choice was already made, nothing closes
  // the wrongly-opened prompt. So: answer once, reload, and the prompt must
  // NOT be open once the real basket is in.
  test('before_item: a reload after the choice was made does not re-ask (the basket loads asynchronously)', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await page.getByTestId('order-type-prompt-dine-in').click();
    await expect(page.locator('#basket')).toHaveAttribute('data-order-type-chosen', 'true');

    await page.goto('/');
    await expect(page.locator('#basket')).toHaveAttribute('data-order-type-chosen', 'true');
    // Settle: give any wrongly-scheduled load-time open its chance to fire.
    await page.waitForTimeout(300);
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#order-type-prompt-modal:modal')).toHaveCount(0);
  });

  // ut-docs#2371 (independent review finding 1): a wedge/camera scan that
  // arrives while the SALE-START prompt is already open must not be
  // dropped. app.js's window-level keydown buffer submits the scan form
  // regardless of focus, the gate cancels that request and calls
  // showOrderTypePromptModal() again -- which must re-arm the resume even
  // though the dialog is already open, so the cashier's choice continues
  // the scan rather than the sale-start no-op. "Scan first, look at the
  // screen second" is the normal sequence at a counter.
  test('before_item: a wedge scan while the sale-start prompt is open is not lost -- answering adds the scanned item', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');
    await expect(page.locator('#order-type-prompt-modal:modal')).toHaveCount(1);

    // A hardware wedge types the whole code in a burst and finishes with
    // Enter; focus is wherever showModal() put it (inside the dialog).
    await page.keyboard.type('5000000000012', { delay: 5 });
    await page.keyboard.press('Enter');
    // Still open (the scan was intercepted, not answered) and nothing landed.
    await expect(page.locator('#order-type-prompt-modal:modal')).toHaveCount(1);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');

    await page.getByTestId('order-type-prompt-takeaway').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveClass(/is-active/);
  });

  test('before_item: a modifier tile is intercepted BEFORE its picker opens; answering opens the picker and the modifier line lands with the chosen order type', async ({ page }) => {
    const seeded = await seedModifierItem(page);
    seededModItem = seeded;

    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');

    // Dismiss the load-time prompt first so the gate path under test here
    // is the modifier-tile one, not the sale-start one.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    // ut-docs#2294: All is the default tab, so this item's shortcut tile is
    // visible via its own dedicated grid copy, not its (hidden) category
    // panel copy -- both exist in the DOM at once, so disambiguate to the
    // one actually on screen.
    const tile = page.locator('.btn-tile:visible', { hasText: seeded.name });
    await expect(tile).toBeVisible();
    await tile.click();

    // Intercepted BEFORE the picker's own GET ever fires.
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await expect(page.locator('#modifier-modal')).not.toBeVisible();

    await page.getByTestId('order-type-prompt-takeaway').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();

    const modifierModal = page.locator('#modifier-modal');
    await expect(modifierModal).toBeVisible();
    await expect(modifierModal).toContainText(seeded.name);

    await modifierModal.locator('.modifier-option', { hasText: seeded.optionName }).locator('input').check();
    await modifierModal.getByRole('button', { name: /Add to cart/i }).click();

    await expect(page.locator('#basket')).toContainText(seeded.name);
    await expect(page.locator('#basket')).toContainText(seeded.optionName);
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toHaveClass(/is-active/);
  });

  // ut-docs#2371: Cancel/Escape both dismiss the sale-start prompt for
  // just this sale (never disabling the per-item gate below), and Escape
  // only works at all now that this is a real showModal() dialog -- a
  // non-modal .show() dialog has no native Escape-to-close wiring.
  test('before_item: Cancel/Escape dismisses the sale-start prompt for this sale, but the first add attempt still asks', async ({ page }) => {
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');

    // Escape dismissed the sale-start nag, not the gate itself -- the
    // very next add attempt on this still-empty, still-unanswered basket
    // is intercepted exactly as before.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();

    await page.getByTestId('order-type-prompt-cancel').click();
    await expect(page.locator('#order-type-prompt-modal')).not.toBeVisible();
    await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
  });

  // ut-docs#2309 (product-owner scope change, outcome 1 taken): confirms
  // the removal itself didn't break basket rendering for a plain multi-line
  // basket -- no per-line control markup renders at all any more, and the
  // basket's own columns (qty/price/total/remove) are all still intact.
  test('the removed per-line control leaves basket rendering otherwise intact', async ({ page }) => {
    await page.goto('/');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await page.locator('.scan-row input[name="code"]').fill('5000000000029');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#basket')).toContainText('Pepsi');

    await expect(page.locator('[class*="line-order-type"]')).toHaveCount(0);
    await expect(page.locator('[data-testid^="line-order-type-"]')).toHaveCount(0);
    await expect(page.locator('.order-type-mixed')).toHaveCount(0);

    // Basket-top bulk toggle is still there and still works.
    await expect(page.locator('[data-testid="order-type-dine-in"]')).toBeVisible();
    await expect(page.locator('[data-testid="order-type-takeaway"]')).toBeVisible();
    await expect(page.locator('tbody#basket-lines tr')).toHaveCount(2);
  });
});
