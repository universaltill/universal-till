import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1021: on the physical till (WebKitGTK 2.52 / labwc / Wayland) a
// touch drag that has nothing to pan in its direction — canonically a
// downward drag while already at the top of the page — falls through
// WebKit's gesture arbitration into a mouse-style TEXT SELECTION drag, and
// once a selection exists every subsequent drag extends it instead of
// panning. The operator experiences "cannot scroll anywhere; dragging
// highlights text". Fix: the operator surface is not a document —
// user-select: none on body, re-enabled only for editable/copyable content
// (app.css, "Touch: the till is an operator surface" block).
//
// HONESTY NOTE (same pattern as designer-search.spec.ts / ut-docs#1170):
// Playwright-driven Chromium does NOT reproduce WebKitGTK's pan-vs-select
// arbitration, so no behavioural tap test here can go red on the real bug.
// The device-verified evidence (synthetic evdev gestures on the Pi 5:
// down-drag-at-top selected text pre-fix and selects nothing post-fix,
// up-drag pans in both) lives on the card and in the code-review record.
// What CAN regress silently is the CSS scoping, so that is what these
// assertions pin: selection disabled at the root, restored exactly where
// editing/copying is legitimate.

test('page text is not selectable outside editable fields (ut-docs#1021)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');

  const styles = await page.evaluate(() => {
    const us = (el: Element | null) =>
      el ? getComputedStyle(el as HTMLElement).userSelect : 'MISSING';
    const input = document.querySelector('input');
    const code = document.querySelector('code');
    return {
      body: us(document.body),
      heading: us(document.querySelector('h1, h2')),
      input: us(input),
      // Loud, not skippable (independent review): if /settings ever stops
      // rendering the store/device ids in <code>, this must FAIL so the
      // code/pre exemption can't silently rot — pick a new anchor then.
      code: us(code),
    };
  });

  expect(styles.body).toBe('none');
  // Headings and prose inherit the body's none — the selection fall-through
  // needs selectable text under the finger, and this removes it everywhere.
  expect(styles.heading).toBe('none');
  // Editable fields must keep native selection or typing/caret UX breaks.
  expect(styles.input).toBe('text');
  // Playwright's desktop Chromium matches (pointer: fine), so the copyable
  // <code> exemption applies here; on a coarse-pointer till it is 'none' by
  // design (see app.css) — this assertion covers the desktop side only.
  expect(styles.code).toBe('text');

  assertClean();
});

test('sale screen inherits the same non-selectable surface (ut-docs#1021)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  const bodySelect = await page.evaluate(() => getComputedStyle(document.body).userSelect);
  expect(bodySelect).toBe('none');

  // The barcode input is the sale screen's one legitimate text-entry point.
  const barcode = page.locator('input[name="code"]');
  await expect(barcode).toBeVisible();
  expect(await barcode.evaluate((el) => getComputedStyle(el).userSelect)).toBe('text');

  assertClean();
});
