import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setOskMode, openNewItemForm } from './helpers';

// ut-docs#1835: osk.js's Shift was a plain one-shot toggle — no caps lock —
// so a German merchant typing catalog item names (every noun capitalised)
// had to tap Shift once per letter. Fix: two Shift taps within
// SHIFT_LATCH_MS (400ms), with no character typed in between, latch caps
// on until Shift is tapped a third time; a single tap keeps the exact
// pre-existing one-shot behaviour.

const shiftKey = (page: Page) => page.locator('#osk button[data-k="⇧"]');

async function typeViaOsk(page: Page, chars: string) {
  for (const ch of chars) {
    await page.locator(`#osk button[data-k="${ch}"]`).click();
  }
}

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('double-tapping Shift latches caps: every following letter stays uppercase until Shift is tapped again', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  // Two taps back-to-back, well inside the 400ms window.
  await shiftKey(page).click();
  await shiftKey(page).click();
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-on/);

  await typeViaOsk(page, 'abc');
  await expect(name).toHaveValue('ABC');
  // Still latched — inserting characters must not clear it (unlike the
  // one-shot, which the sibling test below asserts DOES clear).
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);

  // A third Shift tap clears the latch back to no-shift (not back to a
  // fresh one-shot).
  await shiftKey(page).click();
  await expect(shiftKey(page)).not.toHaveClass(/osk-caps/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-on/);
  await typeViaOsk(page, 'd');
  await expect(name).toHaveValue('ABCd');

  assertClean();
});

test('a single Shift tap keeps today\'s one-shot behaviour exactly (no regression)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  await shiftKey(page).click();
  await expect(shiftKey(page)).toHaveClass(/osk-on/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-caps/);

  await typeViaOsk(page, 'a');
  await expect(name).toHaveValue('A');
  // One-shot clears itself after exactly one character — the pre-existing
  // behaviour this fix must not disturb.
  await expect(shiftKey(page)).not.toHaveClass(/osk-on/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-caps/);

  await typeViaOsk(page, 'b');
  await expect(name).toHaveValue('Ab');

  assertClean();
});

test('a slow second Shift tap (outside the latch window) just toggles shift off, same as a single tap always did', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  await shiftKey(page).click();
  await expect(shiftKey(page)).toHaveClass(/osk-on/);
  await page.waitForTimeout(500); // past the 400ms latch window
  await shiftKey(page).click();
  await expect(shiftKey(page)).not.toHaveClass(/osk-on/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-caps/);

  await typeViaOsk(page, 'a');
  await expect(name).toHaveValue('a');

  assertClean();
});

test('closing the keyboard clears the latch', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  await shiftKey(page).click();
  await shiftKey(page).click();
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);

  // Focus a non-OSK-able control (a checkbox) to close the keyboard, same
  // mechanism a real operator moving on to something else would trigger.
  await page.locator('#item-weighed').click();
  await expect(page.locator('#osk')).not.toBeVisible({ timeout: 2000 });

  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(shiftKey(page)).not.toHaveClass(/osk-caps/);
  await expect(shiftKey(page)).not.toHaveClass(/osk-on/);
  await typeViaOsk(page, 'a');
  await expect(name).toHaveValue('a');

  assertClean();
});

test('the latch survives a round trip through the ?123 symbol layer, and survives backspace (independent review, ut-docs#1835)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  await shiftKey(page).click();
  await shiftKey(page).click();
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);

  // ?123 has no Shift key at all (LAYOUTS.sym), so the latch has nothing
  // to render there — it must still be in effect once ABC brings the
  // letter layout back.
  await page.locator('#osk button[data-k="?123"]').click();
  await expect(page.locator('#osk button[data-k="⇧"]')).toHaveCount(0);
  await page.locator('#osk button[data-k="ABC"]').click();
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);

  await typeViaOsk(page, 'a');
  await expect(name).toHaveValue('A');
  // Backspace removes the character but must not touch the latch.
  await page.locator('#osk button[data-k="⌫"]').click();
  await expect(name).toHaveValue('');
  await expect(shiftKey(page)).toHaveClass(/osk-caps/);
  await typeViaOsk(page, 'b');
  await expect(name).toHaveValue('B');

  assertClean();
});
