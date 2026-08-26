import { test, expect } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#1022: osk.js used to suppress the native OS keyboard only
// reactively, inside show() — which runs from a click, by which point the
// browser has already decided, AT FOCUS TIME, whether to open its own IME.
// Every field without its own one-off template guard (28 of 30 pages) could
// therefore pop both keyboards on the very first tap. The fix sweeps every
// OSK-able field up front, before any interaction, so this covers a page
// that never had — and per the fix, should never need — a per-template
// `{{ if ne (oskmode) "off" }}inputmode="none"{{ end }}` guard of its own.
// /catalog is a representative page from the issue's own "0 guards" list.

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('every OSK-able field gets inputmode="none" up front, before any interaction', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  // #item-name never had, and (per the fix) doesn't need, its own template
  // guard — exactly the class of field the double-keyboard bug hit.
  const name = page.locator('#item-name');
  await expect(name).not.toBeFocused(); // never interacted with yet
  await expect(name).toHaveAttribute('inputmode', 'none');

  assertClean();
});

test('a numeric-inputmode text field still gets the numeric OSK layout despite being pre-guarded to inputmode="none"', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  // #item-barcode is type="text" inputmode="numeric" — isNumeric() has to
  // read the ORIGINAL inputmode, not the live "none" the up-front guard
  // just overwrote it with, or every such field would silently fall back
  // to the letter layout.
  const barcode = page.locator('#item-barcode');
  await expect(barcode).toHaveAttribute('inputmode', 'none'); // pre-guarded like any other field

  await barcode.click();
  await expect(page.locator('#osk')).toBeVisible();
  const keyCount = await page.locator('#osk .osk-key').count();
  expect(keyCount, 'the numeric layout has far fewer keys than a full qwerty layout').toBeLessThan(20);
  await expect(page.locator('#osk button[data-k="q"]')).toHaveCount(0);

  assertClean();
});

test('re-tapping a field after visiting another one does not let the native keyboard race back in (ut-docs#1022 regression)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  const name = page.locator('#item-name');
  const sku = page.locator('#item-sku');

  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(name).toHaveAttribute('inputmode', 'none');

  // Retargeting to another field used to call restoreInputmode() on the one
  // being left, releasing its inputmode override — which reopened the
  // exact first-tap race this fix closes for the SECOND tap onward.
  await sku.click();
  await expect(sku).toBeFocused();
  await expect(page.locator('#osk')).toBeVisible();
  // The actual regression: the field just left behind must KEEP its guard.
  // The old code's restoreInputmode(current) call here removed `name`'s
  // inputmode attribute entirely — this assertion is the one thing in this
  // test that fails against the pre-fix osk.js; every assertion before and
  // after it also passes on the old, buggy code.
  await expect(name).toHaveAttribute('inputmode', 'none');

  await name.click();
  await expect(name).toBeFocused();
  await expect(name).toHaveAttribute('inputmode', 'none');

  assertClean();
});

test('OSK mode off never forces inputmode anywhere, and leaves real inputmode values alone', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'off');

  await page.goto('/catalog');
  expect(await page.locator('#item-name').getAttribute('inputmode')).toBeNull();
  // The field's own real, page-authored inputmode is untouched — osk.js
  // returns before it ever runs when mode is "off".
  expect(await page.locator('#item-barcode').getAttribute('inputmode')).toBe('numeric');

  assertClean();
});

test('a field added to the page after load gets the same up-front guard (htmx swaps and any other DOM mutation)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  // Two shapes of "content arrives after load": the added node IS the
  // field itself (an htmx outerHTML swap whose response root is the input,
  // or a bare oob swap — exercises guardField(node) directly, called on
  // every added node by the observer), and the field arrives NESTED inside
  // a wrapper (an htmx innerHTML/beforeend swap of a fragment, by far the
  // more common real shape — exercises the observer's guardSweep(node)
  // branch instead). Both must be caught without a page-specific call site.
  await page.evaluate(() => {
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'osk-test-late-field';
    document.body.appendChild(input);

    const wrapper = document.createElement('div');
    wrapper.innerHTML = '<input type="text" id="osk-test-late-nested-field">';
    document.body.appendChild(wrapper);
  });
  await expect(page.locator('#osk-test-late-field')).toHaveAttribute('inputmode', 'none');
  await expect(page.locator('#osk-test-late-nested-field')).toHaveAttribute('inputmode', 'none');

  assertClean();
});

test('a field added directly is NOT guarded while OSK is disabled (ut-docs#1022 review)', async ({ page }) => {
  const assertClean = watchConsole(page);
  // 'auto' (the default) stays disabled on this browser — no touch, per
  // settings-osk.spec.ts's own "Auto hides it on non-touch (this browser)
  // BY DESIGN" — so this exercises `enabled === false`.
  await setOskMode(page, 'auto');

  await page.goto('/catalog');
  // The MutationObserver calls guardField(node) directly on every added
  // node, bypassing guardSweep()'s own `if (!enabled) return`. Without the
  // same gate inside guardField() itself, a field arriving as a top-level
  // added node (not nested — see the OSK-off test above for the sweep's
  // own gate) would get suppressed even with the OSK fully disabled.
  await page.evaluate(() => {
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'osk-test-disabled-field';
    document.body.appendChild(input);
  });
  expect(await page.locator('#osk-test-disabled-field').getAttribute('inputmode')).toBeNull();

  assertClean();
});

test('a field disabled at sweep time gets guarded once it is enabled in place, with no further DOM mutation (ut-docs#1050)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  // wantsOSK() correctly skips a disabled field at sweep time — but until
  // this fix, nothing re-swept it if it was later flipped enabled via the
  // `.disabled` IDL property alone (no childList mutation for the
  // MutationObserver's own childList/subtree watch to catch), leaving it
  // permanently unguarded for the life of the page.
  await page.evaluate(() => {
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'osk-test-disabled-then-enabled';
    input.disabled = true;
    document.body.appendChild(input);
  });
  const field = page.locator('#osk-test-disabled-then-enabled');
  await expect(field).toHaveAttribute('disabled', '');
  expect(await field.getAttribute('inputmode')).toBeNull(); // not guarded while disabled

  await page.evaluate(() => {
    (document.getElementById('osk-test-disabled-then-enabled') as HTMLInputElement).disabled = false;
  });
  await expect(field).toHaveAttribute('inputmode', 'none');

  assertClean();
});

test('a non-numeric field is left alone on a locale osk.js has no layout for; a numeric field is still guarded (ut-docs#1022 review)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  // LAYOUTS covers en/tr/fa/ar only — de ships as a language plugin and is
  // the German pilot's own locale (CLAUDE.md). ?lang= sets <html lang>
  // directly without requiring the plugin to actually be installed.
  await page.goto('/catalog?lang=de');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');

  // Suppressing the native keyboard here, with no OSK layout able to
  // replace it, would leave the operator with NO way to type "Käse" at
  // all — strictly worse than the pre-fix double-keyboard bug.
  expect(await page.locator('#item-name').getAttribute('inputmode')).toBeNull();

  // Numeric entry is locale-independent (the 'num' layer is just digits),
  // so a numeric-inputmode field is still guarded and routed to it.
  const barcode = page.locator('#item-barcode');
  await expect(barcode).toHaveAttribute('inputmode', 'none');
  await barcode.click();
  await expect(page.locator('#osk')).toBeVisible();
  const keyCount = await page.locator('#osk .osk-key').count();
  expect(keyCount, 'the numeric layout has far fewer keys than a full qwerty layout').toBeLessThan(20);

  assertClean();
});
