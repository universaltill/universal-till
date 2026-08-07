import { test, expect } from '../support/fixtures';

// The 🐞 bug-report panel (ut-docs#346): non-modal capture panel opened
// from the nav on every staff page. Mirrors the e2e/ suite's coverage so a
// regression in either till setup is caught (a prior cycle shipped one by
// running only one of the two Playwright suites).

test('🐞 nav button opens the non-modal panel', async ({ page }) => {
  await page.goto('/');

  const toggle = page.getByTestId('bugreport-toggle');
  await expect(toggle).toBeVisible();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeHidden();

  await toggle.click();
  await expect(panel).toBeVisible();

  // Non-modal: no backdrop element exists, and the page underneath still
  // takes keystrokes — type into the sale screen's barcode input while the
  // panel stays open.
  const barcode = page.getByLabel('Barcode');
  await barcode.click();
  await barcode.fill('PANEL-OPEN-TYPING');
  await expect(barcode).toHaveValue('PANEL-OPEN-TYPING');
  await expect(panel).toBeVisible();

  // Dismissible: the ✕ collapses it.
  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();
});

test('a typed report sends inline without a page navigation', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  await page.locator('#ir-note').fill('e2e(:8080): typed report via the panel');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/issue-reports')),
    page.locator('#ir-save-btn').click(),
  ]);

  await expect(page.locator('#ir-status')).toContainText('Saved');
  await expect(page).toHaveURL(/\/$/); // never navigated away
});

// Same regression lock as e2e/tests/bugreport-panel.spec.ts, on the second
// till setup: the capture form's one-press path (note field + Save) must be
// inside the panel's visible box the moment it opens, not below its own
// inner scroll line (independent review, 2026-08-06).
test('the note field and Save are visible the moment the panel opens', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();

  const panel = await page.getByTestId('bugreport-panel').boundingBox();
  const note = await page.locator('#ir-note').boundingBox();
  const save = await page.locator('#ir-save-btn').boundingBox();
  for (const b of [note!, save!]) {
    expect(b.y).toBeGreaterThanOrEqual(panel!.y - 1);
    expect(b.y + b.height).toBeLessThanOrEqual(panel!.y + panel!.height + 1);
  }
});

test('/report-issue still 200s with the panel already open', async ({ page }) => {
  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  await expect(page.locator('#ir-note')).toHaveCount(1); // no duplicated capture UI
});

// Same regression lock as e2e/tests/bugreport-panel.spec.ts, on the second
// till setup: a dismissal must survive a full-page navigation (this app has
// no hx-boost), including back to /report-issue itself, which otherwise
// force-opens the panel on every single visit regardless of a prior close.
test('closing the panel sticks across a navigation, even back to /report-issue', async ({ page }) => {
  await page.goto('/');
  const panel = page.getByTestId('bugreport-panel');

  await page.getByTestId('bugreport-toggle').click();
  await expect(panel).toBeVisible();
  await page.getByTestId('bugreport-close').click();
  await expect(panel).toBeHidden();

  const resp = await page.goto('/report-issue');
  expect(resp!.status()).toBe(200);
  await expect(panel).toBeHidden();

  // Re-opening explicitly still works — and comes back wired, not just
  // visible: the suppression branch skips the lazy initCapture() the
  // forced-open path used to run, so a real save is driven here to prove
  // the panel isn't re-opening with dead buttons.
  await page.getByTestId('bugreport-toggle').click();
  await expect(panel).toBeVisible();

  await page.locator('#ir-note').fill('e2e(:8080): re-opened after a dismissal');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/issue-reports')),
    page.locator('#ir-save-btn').click(),
  ]);
  await expect(page.locator('#ir-status')).toContainText('Saved');
});

// Same regression lock as e2e/tests/bugreport-panel.spec.ts, on the second
// till setup: ut-docs#395 — the panel must be movable off whatever it's
// covering, dragged from the head bar, not the ✕ (which must keep closing).
test('the panel can be dragged to a different position by its head bar', async ({ page }) => {
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
  // review), so this proves the drag mechanism without also having to
  // re-derive a safe drop zone for this viewport.
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX - 250, startY, { steps: 8 });
  await page.mouse.up();

  const after = (await panel.boundingBox())!;
  expect(after.x).toBeLessThan(before.x - 150);
  expect(Math.abs(after.y - before.y)).toBeLessThan(5);

  await expect(panel).toBeVisible();
  const barcode = page.getByLabel('Barcode');
  await barcode.click();
  await barcode.fill('DRAG-DID-NOT-TRAP-FOCUS');
  await expect(barcode).toHaveValue('DRAG-DID-NOT-TRAP-FOCUS');
});

test('dragging the head does not trigger the ✕ close control', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeVisible();

  const close = page.getByTestId('bugreport-close');
  const closeBox = (await close.boundingBox())!;
  const head = page.locator('.bugreport-head h2');
  const handle = (await head.boundingBox())!;
  await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2);
  await page.mouse.down();
  await page.mouse.move(closeBox.x + closeBox.width / 2, closeBox.y + closeBox.height / 2, { steps: 6 });
  await page.mouse.up();
  await expect(panel).toBeVisible();
});

// Mirrors the touch + off-screen coverage added to
// e2e/tests/bugreport-panel.spec.ts by the ut-docs#395 review. page.mouse
// only simulates a mouse, so the touchscreen path this feature exists for
// is driven through CDP instead (Chromium-only, which this project is).
async function touchDriver(page: import('@playwright/test').Page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 });
  return (type: 'touchStart' | 'touchMove' | 'touchEnd' | 'touchCancel', pts: { x: number; y: number }[]) =>
    cdp.send('Input.dispatchTouchEvent', { type, touchPoints: pts as never });
}

test('touch: the panel drags, and a cancelled gesture does not leave it stuck to the pointer', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  await expect(panel).toBeVisible();
  const touch = await touchDriver(page);

  const grab = async () => {
    const h = (await page.locator('.bugreport-head h2').boundingBox())!;
    return { x: h.x + h.width / 2, y: h.y + h.height / 2 };
  };

  const before = (await panel.boundingBox())!;
  let p = await grab();
  await touch('touchStart', [p]);
  for (let i = 1; i <= 5; i++) await touch('touchMove', [{ x: p.x - i * 40, y: p.y }]);
  await touch('touchEnd', []);
  const dragged = (await panel.boundingBox())!;
  expect(dragged.x).toBeLessThan(before.x - 150);

  // A gesture the browser cancels (palm rejection, an extra touch point, a
  // system swipe) must end the drag — unhandled, the panel kept following
  // the pointer afterwards with nothing pressed.
  p = await grab();
  await touch('touchStart', [p]);
  await touch('touchMove', [{ x: p.x - 50, y: p.y + 20 }]);
  await touch('touchCancel', []);
  const parked = (await panel.boundingBox())!;
  await page.evaluate(() =>
    document.dispatchEvent(new PointerEvent('pointermove', {
      bubbles: true, pointerId: 991, clientX: 120, clientY: 520, buttons: 0,
    })));
  const still = (await panel.boundingBox())!;
  expect(still.x).toBeCloseTo(parked.x, 0);
  expect(still.y).toBeCloseTo(parked.y, 0);
});

test('a dragged panel is pulled back on-screen when the viewport shrinks', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/');
  await page.getByTestId('bugreport-toggle').click();
  const panel = page.getByTestId('bugreport-panel');
  const h = (await page.locator('.bugreport-head h2').boundingBox())!;
  await page.mouse.move(h.x + h.width / 2, h.y + h.height / 2);
  await page.mouse.down();
  await page.mouse.move(h.x + h.width / 2 - 100, h.y + h.height / 2 + 300, { steps: 8 });
  await page.mouse.up();
  // Also the DOWNWARD drag the horizontal-only test above can't cover: it
  // crosses the till's own .btn anchors, which used to start a native
  // link drag-and-drop and cancel the pointer one frame in.
  expect((await panel.boundingBox())!.y).toBeGreaterThan(300);

  await page.setViewportSize({ width: 1024, height: 400 });
  const after = (await panel.boundingBox())!;
  expect(after.y + after.height).toBeLessThanOrEqual(400 + 1);
  expect(after.x + after.width).toBeLessThanOrEqual(1024 + 1);
  expect(after.x).toBeGreaterThanOrEqual(-1);
  expect(after.y).toBeGreaterThanOrEqual(-1);
});
