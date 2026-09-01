import { test, expect } from './fixtures';
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

// ut-docs#1262: a device whose touch is misreported as mouse input (the
// same class ut-docs#1238 found — an Android tablet in "desktop site" mode
// reads as a generic desktop to matchMedia/maxTouchPoints, and its real
// taps dispatch as mouse-compatibility events, never a genuine touchstart)
// used to never flip `enabled` at all in 'auto' mode: only a real
// touchstart did. This browser (the default project, no `hasTouch`) is
// itself a stand-in for exactly that failure mode — its plain mouse click
// below never fires touchstart either, so pre-fix this test's own click
// would leave `enabled` false forever, same as the misdetected device.
test('a plain click (no touchstart) still enables auto mode, for a device whose touch never fires one (ut-docs#1262)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'auto');

  await page.goto('/catalog');
  const name = page.locator('#item-name');
  // Pre-fix: 'auto' + no touchstart ever fired (this browser has no touch)
  // meant `enabled` stayed false forever, so clicking here would never
  // suppress the native keyboard or open the custom one — the identical
  // silent gap ut-docs#1262 reports on the misdetected hardware.
  //
  // The fix's own enable is deferred one tick past this first click (see
  // osk.js's `enableFallback` comment — running guardSweep()/
  // updateToggles() synchronously mid-click reveals the scan-row's OSK
  // toggle and can shift layout under a still-in-flight click elsewhere on
  // the page), so this first click does NOT itself pop the OSK — only
  // `toHaveAttribute` below (which polls) proves the field is guarded once
  // the deferred enable has run.
  await name.click();
  await expect(name).toHaveAttribute('inputmode', 'none');

  // A SECOND interaction, now that `enabled` is true from the moment it
  // starts, opens the OSK exactly like the already-covered touchstart path.
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  // The fix enables from the source (any click, anywhere), not just this
  // one field — a field added afterward is swept too.
  await page.evaluate(() => {
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'osk-test-1262-late-field';
    document.body.appendChild(input);
  });
  await expect(page.locator('#osk-test-1262-late-field')).toHaveAttribute('inputmode', 'none');

  assertClean();
});

// ut-docs#1262 review (blocking finding, fixed before merge): an earlier
// draft enabled from `pointerdown` and deferred the reflow one tick via
// `setTimeout`, reasoning that a tick was enough to clear the current
// interaction — true only for a synthetic `.click()`'s sub-millisecond
// mousedown-to-mouseup gap, false for any real, human-scale press.
// `pointerdown` fires WHILE the interaction is still in flight (mouseup/
// click that follow are each hit-tested independently at a fixed
// coordinate — no implicit capture the way a real touch tap gets), so on a
// real press the timeout fired BEFORE mouseup/click, not after: reveals the
// scan-row's OSK toggle (which sits after the Add button, shifting it) mid-
// press, silently dropping the tap — precisely the misdetected hardware
// this ticket is about, which holds contact for real duration, not an
// instant. The fix moved the trigger to `click` itself, which only ever
// fires once this interaction's own hit-testing is already resolved. `{
// delay: 120 }` below is the load-bearing part of this test: a plain
// `.click()` (no delay) would have passed even against the buggy
// `pointerdown`+`setTimeout` draft, which is exactly why that draft's own
// version of this test didn't catch the bug.
test('a realistic-duration press (not an instant synthetic click) is not hijacked by the fix enabling mid-press (ut-docs#1262 review)', async ({ page }) => {
  const assertClean = watchConsole(page);
  // The basket lives in the server-global engine, shared by every spec on
  // this till (ut-docs#1177 review, F2) — reset both sides so a line left
  // behind here doesn't leak into whatever runs next either.
  await page.request.post('/api/pos/reset');
  await setOskMode(page, 'auto');

  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/')),
    page.locator('.scan-row button[type=submit]').click({ delay: 120 }),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.request.post('/api/pos/reset');
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

  // LAYOUTS covers en/tr/fa/ar/de/es only (ut-docs#1047 added de/es) — 'zz'
  // is not a real locale and never will be, so this keeps exercising the
  // fallback path itself rather than a specific locale that might one day
  // gain a layout of its own. ?lang= sets <html lang> directly without
  // requiring any matching language plugin to actually be installed.
  await page.goto('/catalog?lang=zz');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');

  // Suppressing the native keyboard here, with no OSK layout able to
  // replace it, would leave the operator with NO way to type at all —
  // strictly worse than the pre-fix double-keyboard bug.
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

// ut-docs#1047: de/es ship as language plugins (ut-plugin-language-{de,es})
// with no OSK layout of their own until this fix — LAYOUTS fell back to 'en'
// (baseLayout()) and localeSupported() (ut-docs#1022) treated them as
// unsupported, leaving Germany's pilot shops with no way to type ä/ö/ü/ß via
// the OSK at all. These two tests drive real key taps, not just attribute
// checks, because a wrong key POSITION (e.g. a QWERTY 'de' layout instead of
// the real QWERTZ z/y swap) would pass an attribute-only test just as easily
// as a correct layout.
test('de OSK renders a real QWERTZ layout and types umlauts/ß (ut-docs#1047)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog?lang=de');
  // localeSupported() now recognises 'de', so the native keyboard goes back
  // to being suppressed for non-numeric fields too (the opposite assertion
  // from the 'zz' test above — this is the fix this locale used to lack).
  const name = page.locator('#item-name');
  await expect(name).toHaveAttribute('inputmode', 'none');

  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  // QWERTZ, not just "z and y both exist somewhere": data-k lookups are
  // positionless (press() reads whichever button was clicked regardless of
  // where it sits), so asserting mere presence would pass even against an
  // un-swapped QWERTY 'de' layout. Assert real row order instead — the top
  // letter row must have 'z' where QWERTY has 'y' (between t and u), and the
  // bottom letter row must have 'y' where QWERTY has 'z' (right after ⇧).
  const rows = page.locator('#osk .osk-row');
  const topRowKeys = await rows.nth(1).locator('button').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.k));
  expect(topRowKeys, 'top letter row must be real QWERTZ order').toEqual(
    ['q', 'w', 'e', 'r', 't', 'z', 'u', 'i', 'o', 'p', 'ü'],
  );
  const shiftRowKeys = await rows.nth(3).locator('button').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.k));
  expect(shiftRowKeys, 'shift/bottom-letter row must be real QWERTZ order').toEqual(
    ['⇧', 'y', 'x', 'c', 'v', 'b', 'n', 'm', 'ß', '⌫'],
  );

  await page.locator('#osk button[data-k="k"]').click();
  await page.locator('#osk button[data-k="ä"]').click();
  await page.locator('#osk button[data-k="s"]').click();
  await page.locator('#osk button[data-k="e"]').click();
  await expect(name).toHaveValue('käse');

  // ß has no traditional single-character uppercase — the default (and
  // German-locale) Unicode case mapping is the two-character "SS", verified
  // directly in Node against this exact call: 'ß'.toLocaleUpperCase('de')
  // === 'SS'. insert() already accepts multi-character strings via
  // setRangeText, so no special-casing was needed to get this right.
  await name.fill('');
  await page.locator('#osk button[data-k="⇧"]').click();
  await page.locator('#osk button[data-k="ß"]').click();
  await expect(name).toHaveValue('SS');

  assertClean();
});

test('es OSK renders accented vowels/ñ and types real Spanish characters (ut-docs#1047)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog?lang=es');
  const name = page.locator('#item-name');
  await expect(name).toHaveAttribute('inputmode', 'none');

  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  // Real row order (`tr`'s own precedent: append the extra glyphs to the
  // ends of the base QWERTY rows), not just presence — see the `de` test
  // above for why a mere-visibility check would be a weaker assertion.
  const rows = page.locator('#osk .osk-row');
  const topRowKeys = await rows.nth(1).locator('button').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.k));
  // ü appended last (ut-docs#1147: pingüino/bilingüe need it and there's no
  // dead-key mechanism to reach it any other way).
  expect(topRowKeys, 'top letter row must end with the appended á/é/ü').toEqual(
    ['q', 'w', 'e', 'r', 't', 'y', 'u', 'i', 'o', 'p', 'á', 'é', 'ü'],
  );
  const homeRowKeys = await rows.nth(2).locator('button').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.k));
  // ñ directly after l (its real physical/mobile-keyboard position), not
  // after í — an earlier draft had these swapped (independent review,
  // ut-docs#1047).
  expect(homeRowKeys, 'home row must have ñ directly after l, then í').toEqual(
    ['a', 's', 'd', 'f', 'g', 'h', 'j', 'k', 'l', 'ñ', 'í'],
  );

  await page.locator('#osk button[data-k="ñ"]').click();
  await page.locator('#osk button[data-k="á"]').click();
  await expect(name).toHaveValue('ñá');

  // Spanish accented vowels uppercase to themselves-with-accent by default
  // (á → Á), unlike German ß — no special-casing needed here either.
  await name.fill('');
  await page.locator('#osk button[data-k="⇧"]').click();
  await page.locator('#osk button[data-k="á"]').click();
  await expect(name).toHaveValue('Á');

  // ü must be reachable and type real güe/güi words (ut-docs#1147).
  await name.fill('');
  await page.locator('#osk button[data-k="p"]').click();
  await page.locator('#osk button[data-k="i"]').click();
  await page.locator('#osk button[data-k="n"]').click();
  await page.locator('#osk button[data-k="g"]').click();
  await page.locator('#osk button[data-k="ü"]').click();
  await page.locator('#osk button[data-k="i"]').click();
  await page.locator('#osk button[data-k="n"]').click();
  await page.locator('#osk button[data-k="o"]').click();
  await expect(name).toHaveValue('pingüino');

  assertClean();
});

// ut-docs#1148: ¿ and ¡ must be reachable via the shared sym layer, not
// just the es-specific layout — every locale benefits from inverted
// punctuation on the ?123 page.
test('sym layer includes inverted punctuation ¿ and ¡ (ut-docs#1148)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog?lang=en');
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  // Switch to the sym (?123) layer.
  await page.locator('#osk button[data-k="?123"]').click();
  await expect(page.locator('#osk')).toBeVisible();

  // Both inverted punctuation marks must exist as keys.
  await expect(page.locator('#osk button[data-k="¿"]')).toBeVisible();
  await expect(page.locator('#osk button[data-k="¡"]')).toBeVisible();

  // They must type into the field.
  await page.locator('#osk button[data-k="¿"]').click();
  await page.locator('#osk button[data-k="¡"]').click();
  await expect(name).toHaveValue('¿¡');

  assertClean();
});
