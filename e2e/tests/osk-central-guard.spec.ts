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
  // Stand-in for content arriving via htmx (or a plugin partial, or Alpine)
  // after the initial sweep already ran — the MutationObserver (and the
  // htmx:afterSwap listener wired to the same guardSweep()) must catch it
  // without a page-specific call site.
  await page.evaluate(() => {
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'osk-test-late-field';
    document.body.appendChild(input);
  });
  await expect(page.locator('#osk-test-late-field')).toHaveAttribute('inputmode', 'none');

  assertClean();
});
