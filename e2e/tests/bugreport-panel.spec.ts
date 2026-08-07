import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// The 🐞 bug-report panel (ut-docs#346): the capture UI moved out of the
// standalone /report-issue page into a floating, NON-modal panel opened
// from the nav. The acceptance criterion that must be driven for real, not
// checked by eye: with the panel open, the page underneath stays fully
// clickable, typeable and scrollable — no backdrop, no focus trap.

test('🐞 opens the panel; closed by default; ✕ closes it again', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  const toggle = page.getByTestId('bugreport-toggle');
  await expect(toggle).toBeVisible(); // auth off ⇒ manager-equivalent
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeHidden(); // no page pays for the panel until used

  await toggle.click();
  await expect(panel).toBeVisible();
  // Permanently-visible expectation note: recording ends on navigation.
  await expect(panel.locator('.bugreport-nav-note')).toBeVisible();

  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();

  // The toggle also collapses it (second click).
  await toggle.click();
  await expect(panel).toBeVisible();
  await toggle.click();
  await expect(panel).toBeHidden();
  assertClean();
});

test('non-modal: a full sale completes underneath the open panel', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();

  // Typeable underneath: the barcode input takes keystrokes with the panel
  // open (a seeded demo barcode, same as sale.spec.ts).
  const barcode = page.locator('.scan-row input[name=code]');
  await barcode.click();
  await barcode.fill('5000000000012');
  await expect(barcode).toHaveValue('5000000000012');

  // Clickable underneath: the scan submits, the basket updates…
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await expect(page.locator('.basket .total')).toContainText('1.20');

  // …and the tender buttons are neither covered nor disabled: cash pays
  // and the receipt view appears, all while the panel stays open.
  await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  assertClean();
});

test('the open panel never overlaps the basket column or the tender panel', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#basket')).toBeVisible(); // htmx load done
  await page.getByTestId('bugreport-toggle').click();

  const panel = await page.getByTestId('bugreport-panel').boundingBox();
  const basket = await page.locator('.pos-container > .basket').boundingBox();
  const tender = await page.locator('.pos-container > .tender').boundingBox();
  expect(panel).not.toBeNull();
  expect(basket).not.toBeNull();
  expect(tender).not.toBeNull();
  // LTR: basket is the start column, panel is anchored at the inline end.
  expect(panel!.x).toBeGreaterThanOrEqual(basket!.x + basket!.width - 1);
  // The panel's bounded max-height keeps it above the tender area even on
  // a short viewport — pay buttons must never sit under it.
  expect(panel!.y + panel!.height).toBeLessThanOrEqual(tender!.y + 1);
});

// Regression lock (independent review, 2026-08-06). Clearing both the basket
// column and the tender panel leaves the panel only ~13rem of vertical band
// on a till-sized screen, and the first cut spent that band on two paragraphs
// of prose: opening 🐞 showed explanatory text and NO input at all, with the
// note field and Save button below the panel's own inner scroll line. The
// one-press path the card is about has to be visible on open, at every size a
// till actually runs at — bounding boxes, not eyeballs.
for (const [w, h] of [[1280, 720], [1024, 600], [1280, 800]] as const) {
  test(`the note field and Save are visible on open at ${w}x${h}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: h });
    await page.goto('/');
    await expect(page.locator('#basket')).toBeVisible();
    await page.getByTestId('bugreport-toggle').click();

    const panel = await page.getByTestId('bugreport-panel').boundingBox();
    const note = await page.locator('#ir-note').boundingBox();
    const save = await page.locator('#ir-save-btn').boundingBox();
    for (const b of [panel, note, save]) expect(b).not.toBeNull();
    // Inside the panel's own visible box — no inner scrolling required.
    for (const b of [note!, save!]) {
      expect(b.y).toBeGreaterThanOrEqual(panel!.y - 1);
      expect(b.y + b.height).toBeLessThanOrEqual(panel!.y + panel!.height + 1);
    }
    // …and still clear of the tender, so this never trades one bug for the other.
    const tender = await page.locator('.pos-container > .tender').boundingBox();
    expect(panel!.y + panel!.height).toBeLessThanOrEqual(tender!.y + 1);
  });
}

test('scrollable underneath: a long page still scrolls with the panel open', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');
  await page.getByTestId('bugreport-toggle').click();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();

  // A real wheel gesture over the page body (not over the panel).
  const before = await page.evaluate(() => window.scrollY);
  await page.mouse.move(200, 400);
  await page.mouse.wheel(0, 600);
  await expect
    .poll(() => page.evaluate(() => window.scrollY))
    .toBeGreaterThan(before);
  assertClean();
});

test('a typed report sends inline — success reported without navigating', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  await page.locator('#ir-note').fill('e2e: typed-only report through the panel');
  await page.locator('#ir-save-btn').click();

  // Outcome lands inline in the panel's status line…
  await expect(page.locator('#ir-status')).toContainText('Saved');
  // …and the page never navigated.
  await expect(page).toHaveURL('http://127.0.0.1:8091/');
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  assertClean();
});

test('/report-issue still works and lands with the panel already open', async ({ page }) => {
  const assertClean = watchConsole(page);
  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  // Open server-side (class stamped in the markup), not via client JS.
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  await expect(page.locator('#ir-note')).toBeVisible();
  // The page body itself no longer duplicates the capture UI: exactly one
  // note textarea on the whole page (the panel's).
  await expect(page.locator('#ir-note')).toHaveCount(1);
  assertClean();
});

// Regression lock for ut-docs#394: this app does full page loads for every
// navigation (no hx-boost), and /report-issue force-opens the panel
// server-side on every visit. A dismissal must survive that — otherwise
// re-landing on /report-issue (a re-tapped /menu tile, a kiosk reload) pops
// the panel back open with no way to make it stay closed, which is exactly
// what the field report described.
test('closing the panel sticks across a navigation, even back to /report-issue', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  const panel = page.getByTestId('bugreport-panel');

  await page.getByTestId('bugreport-toggle').click();
  await expect(panel).toBeVisible();
  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();

  // The forced-open route must honor the dismissal, not override it.
  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  await expect(panel).toBeHidden();

  // An explicit re-open still works — dismissal isn't a one-way dead end.
  await page.getByTestId('bugreport-toggle').click();
  await expect(panel).toBeVisible();

  // …and it comes back FUNCTIONAL, not merely visible (independent review,
  // 2026-08-07). The suppression branch above deliberately skips the lazy
  // initCapture() that the server-forced-open path used to run, so this is
  // the one page state where the panel can exist, un-wired, and then be
  // re-opened: drive a real save to prove the wiring happened on re-open
  // rather than shipping a panel with dead buttons.
  await page.locator('#ir-note').fill('e2e: re-opened after a dismissal');
  await page.locator('#ir-save-btn').click();
  await expect(page.locator('#ir-status')).toContainText('Saved');
  assertClean();
});

// ut-docs#395: the panel must be movable off whatever it's covering — the
// operator has to keep seeing the screen underneath while reproducing a
// problem. Dragged from the head bar, not the ✕ (which must keep closing).
test('the panel can be dragged to a different position by its head bar', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  const panel = page.getByTestId('bugreport-panel');
  const before = (await panel.boundingBox())!;
  const head = page.locator('.bugreport-head h2');
  const handle = (await head.boundingBox())!;
  const startX = handle.x + handle.width / 2;
  const startY = handle.y + handle.height / 2;

  // Horizontal-only: dragging left keeps the panel's vertical band exactly
  // where it started (measured clear of the scan row by the ut-docs#346
  // review — see app.css's own comment on .bugreport-panel), so this proves
  // the drag mechanism without also having to re-derive a safe drop zone
  // for every viewport this suite runs at.
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX - 250, startY, { steps: 8 });
  await page.mouse.up();

  const after = (await panel.boundingBox())!;
  expect(after.x).toBeLessThan(before.x - 150);
  expect(Math.abs(after.y - before.y)).toBeLessThan(5);

  // Dragging never closes it, and the till underneath stays operable — same
  // non-modal guarantee the rest of this suite already locks in.
  await expect(panel).toBeVisible();
  const barcode = page.locator('.scan-row input[name=code]');
  await barcode.click();
  await barcode.fill('DRAG-DID-NOT-TRAP-FOCUS');
  await expect(barcode).toHaveValue('DRAG-DID-NOT-TRAP-FOCUS');
  assertClean();
});

test('dragging the head does not trigger the ✕ close control', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeVisible();

  const close = page.getByTestId('bugreport-close');
  const closeBox = (await close.boundingBox())!;
  // A drag that passes over the close button mid-motion must not register
  // as a click on it — only a pointerdown that STARTS there should close.
  const head = page.locator('.bugreport-head h2');
  const handle = (await head.boundingBox())!;
  await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2);
  await page.mouse.down();
  await page.mouse.move(closeBox.x + closeBox.width / 2, closeBox.y + closeBox.height / 2, { steps: 6 });
  await page.mouse.up();
  await expect(panel).toBeVisible();
});

// The kiosk this panel exists for is a TOUCHSCREEN, and page.mouse only ever
// simulates a mouse — the drag tests above never reach the touch code path.
// Drive real touch through CDP instead (Chromium-only, which this suite
// already is). Two things only touch can reach, both found by the
// independent review of ut-docs#395:
//  - pointercancel: the browser/OS takes a gesture over for palm rejection,
//    an extra touch point or a system swipe. Unhandled, the drag never ended
//    — the document pointermove listener stayed attached for the life of the
//    page and the panel then followed the pointer with nothing pressed,
//    wandering back over the screen the operator had just dragged it off.
//  - a foreign pointer's release: with a second finger on the glass (holding
//    the panel while tapping the till is the workflow this panel is for) the
//    other finger's pointerup ended the drag AND threw an uncaught
//    NotFoundError out of releasePointerCapture.
async function touchDriver(page: import('@playwright/test').Page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 });
  return (type: 'touchStart' | 'touchMove' | 'touchEnd' | 'touchCancel', pts: { x: number; y: number }[]) =>
    cdp.send('Input.dispatchTouchEvent', { type, touchPoints: pts as never });
}

// Nudges the pointer with nothing pressed. If the drag is still live this
// drags the panel — which is exactly the stuck state being guarded against.
const strayPointerMove = (page: import('@playwright/test').Page) =>
  page.evaluate(() =>
    document.dispatchEvent(new PointerEvent('pointermove', {
      bubbles: true, pointerId: 991, clientX: 120, clientY: 520, buttons: 0,
    })));

test('touch: the panel drags, and a cancelled gesture does not leave it stuck to the pointer', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeVisible();
  const touch = await touchDriver(page);

  const grab = async () => {
    const h = (await page.locator('.bugreport-head h2').boundingBox())!;
    return { x: h.x + h.width / 2, y: h.y + h.height / 2 };
  };

  // 1. A plain one-finger drag moves it, same as the mouse.
  const before = (await panel.boundingBox())!;
  let p = await grab();
  await touch('touchStart', [p]);
  for (let i = 1; i <= 5; i++) await touch('touchMove', [{ x: p.x - i * 40, y: p.y }]);
  await touch('touchEnd', []);
  const dragged = (await panel.boundingBox())!;
  expect(dragged.x).toBeLessThan(before.x - 150);

  // 2. A gesture the browser cancels mid-drag must end the drag cleanly.
  p = await grab();
  await touch('touchStart', [p]);
  await touch('touchMove', [{ x: p.x - 50, y: p.y + 20 }]);
  await touch('touchCancel', []);
  const parked = (await panel.boundingBox())!;
  await strayPointerMove(page);
  const still = (await panel.boundingBox())!;
  expect(still.x).toBeCloseTo(parked.x, 0);
  expect(still.y).toBeCloseTo(parked.y, 0);

  // 3. …and the panel is still draggable afterwards, not wedged.
  p = await grab();
  await touch('touchStart', [p]);
  for (let i = 1; i <= 4; i++) await touch('touchMove', [{ x: p.x + i * 30, y: p.y }]);
  await touch('touchEnd', []);
  expect((await panel.boundingBox())!.x).toBeGreaterThan(parked.x + 60);
  assertClean();
});

test('touch: another pointer releasing mid-drag neither ends the drag nor throws', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeVisible();
  const touch = await touchDriver(page);

  const h = (await page.locator('.bugreport-head h2').boundingBox())!;
  const x = h.x + h.width / 2, y = h.y + h.height / 2;
  await touch('touchStart', [{ x, y }]);
  await touch('touchMove', [{ x: x - 40, y }]);
  const mid = (await panel.boundingBox())!;

  // A different pointer lifts somewhere else on the page (second finger).
  await page.evaluate(() =>
    document.dispatchEvent(new PointerEvent('pointerup', {
      bubbles: true, pointerId: 4242, pointerType: 'touch', clientX: 200, clientY: 600, button: 0, buttons: 0,
    })));

  // The finger that owns the drag keeps going and must still be steering.
  await touch('touchMove', [{ x: x - 240, y }]);
  const end = (await panel.boundingBox())!;
  await touch('touchEnd', []);
  expect(end.x).toBeLessThan(mid.x - 100);
  assertClean(); // no uncaught NotFoundError from releasePointerCapture
});

test('a dragged panel is pulled back on-screen when the viewport shrinks', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  const h = (await page.locator('.bugreport-head h2').boundingBox())!;
  await page.mouse.move(h.x + h.width / 2, h.y + h.height / 2);
  await page.mouse.down();
  await page.mouse.move(h.x + h.width / 2 - 100, h.y + h.height / 2 + 300, { steps: 8 });
  await page.mouse.up();
  // This is also the only DOWNWARD drag in the suite, and it earns its keep
  // twice: dragging down crosses the till's own .btn anchors, which started
  // a native link drag-and-drop and cancelled the pointer one frame in. The
  // horizontal-only drags above never reached that.
  expect((await panel.boundingBox())!.y).toBeGreaterThan(300);

  // A desktop window resize / a tablet till rotating must not strand the
  // panel outside the viewport, where there is no head bar left to grab.
  await page.setViewportSize({ width: 1024, height: 400 });
  const after = (await panel.boundingBox())!;
  expect(after.y + after.height).toBeLessThanOrEqual(400 + 1);
  expect(after.x + after.width).toBeLessThanOrEqual(1024 + 1);
  expect(after.x).toBeGreaterThanOrEqual(-1);
  expect(after.y).toBeGreaterThanOrEqual(-1);
  assertClean();
});
