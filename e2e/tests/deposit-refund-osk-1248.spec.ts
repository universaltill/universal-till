import { test, expect } from '@playwright/test';
import { setOskMode } from './helpers';

// ut-docs#1248: "when I click on deposit refund, 2 keyboards, opening."
//
// Root cause: <dialog id="pfand-modal">.show() was silently moving DOM
// focus to #pfand-amount even though the field carries no `autofocus`
// attribute — verified live (this repo's own real Chromium build; not a
// spec assumption): document.activeElement was "pfand-amount" immediately
// after .show(), before any tap. That's exactly the "programmatic focus"
// this app's own OSK design says must never pop a keyboard (osk.js's own
// ut-docs#155 comment) — deliberate autofocus elsewhere (#hold-label-input)
// is explicitly accounted for; this one wasn't designed at all, just an
// engine default nobody noticed.
//
// While OSK's up-front sweep (ut-docs#1022) already covers this field by
// the time the dialog opens on the HAPPY path (real touchstart precedes
// the click that calls .show()), the sweep only ever runs once `enabled`
// flips true — which, in the default 'auto' mode, waits for a genuine
// `touchstart` event. A device whose touch is misreported as mouse input
// (confirmed real and current on this exact hardware class: ut-docs#1238,
// Android Chrome's desktop-site mode on a large tablet) never fires
// touchstart at all, so the sweep never runs, so the accidental focus this
// fix removes would have opened the UNSUPPRESSED native keyboard for
// #pfand-amount — indistinguishable, from the operator's seat, from "the
// screen just showed 2 keyboards" the moment they then deliberately tap a
// field and this app's own OSK opens on top of it.
//
// The fix removes the accidental focus outright (blur after .show()), so
// nothing is ever focused — keyboard or not — until the operator deliberately
// taps a field, exactly like every other non-autofocus field in the app.

async function openPfand(page: import('@playwright/test').Page) {
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('[data-testid="kiosk-pfand-open"]').click();
  await expect(page.locator('#pfand-modal')).toBeVisible();
}

test('opening the deposit-refund dialog focuses nothing — no field is auto-focused', async ({ page }) => {
  await openPfand(page);
  const active = await page.evaluate(() => document.activeElement?.id || document.activeElement?.tagName);
  expect(active, 'no field should be focused until the operator deliberately taps one').not.toBe('pfand-amount');
  expect(active, 'no field should be focused until the operator deliberately taps one').not.toBe('manager_pin');
});

test('deposit-refund dialog: only one keyboard ever opens, one field at a time', async ({ page }) => {
  await setOskMode(page, 'on');
  await openPfand(page);

  // Never opened at all before a deliberate tap — the regression this
  // ticket is about.
  expect(await page.locator('#osk').count()).toBe(0);

  await page.locator('#pfand-amount').click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  expect(await page.locator('#osk').count(), 'exactly one keyboard element ever exists').toBe(1);

  // Moving to the second field re-targets the SAME singleton keyboard, not
  // a second one.
  await page.locator('[name="manager_pin"]').click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  expect(await page.locator('#osk').count(), 'still exactly one keyboard element').toBe(1);
});
