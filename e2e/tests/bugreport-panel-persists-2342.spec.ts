import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2342: an open bug-report panel is a DRAFT that belongs to the till
// session, not to the page. It survives a boosted rail navigation (the DOM
// itself persists — ADR-0098), AND a full document load (a form POST, a
// language/theme change, a self-update, the panel's own /my-reports link):
// text, screenshots, a finished voice/screen recording, the dragged
// position and the open/closed state are restored on the next page. A
// recording in progress across a FULL load cannot keep running — what was
// captured up to the reload is kept and the panel says so; it is never
// silently dropped. Nothing but Send / Discard / the ✕ ever closes it.

// A 1x1 PNG. The Android app's capture path (`window.AndroidKiosk
// .captureScreenshot`) is the one code path that yields a screenshot without
// a getDisplayMedia picker, so the tests fake that bridge — it exercises the
// real thumbnail + persistence code, not a test-only seam.
const PNG_1x1 =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

const fakeAndroidScreenshot = `window.AndroidKiosk = { captureScreenshot: function () { return ${JSON.stringify(PNG_1x1)}; } };`;

// getDisplayMedia replaced with a real MediaStream from a canvas, so the
// REAL MediaRecorder runs (timeslice chunks, IndexedDB persistence, the
// stop/assemble path) — only the picker is bypassed.
const fakeDisplayMedia = `
  (function () {
    var c = document.createElement('canvas'); c.width = 64; c.height = 64;
    var ctx = c.getContext('2d'); var n = 0;
    setInterval(function () { ctx.fillStyle = (n++ % 2) ? '#f00' : '#00f'; ctx.fillRect(0, 0, 64, 64); }, 100);
    navigator.mediaDevices.getDisplayMedia = function () { return Promise.resolve(c.captureStream(10)); };
  })();`;

test.describe('bug-report panel persists across pages (ut-docs#2342)', () => {
  test('draft survives a full document load: open state, note, screenshots', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeAndroidScreenshot);
    await page.goto('/');
    const panel = page.getByTestId('bugreport-panel');
    await page.getByTestId('bugreport-toggle').click();
    await expect(panel).toBeVisible();

    await page.locator('#ir-note').fill('e2e: draft written on the sale screen');
    await page.locator('#ir-screenshot-btn').click();
    await page.locator('#ir-screenshot-btn').click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(2);

    // A FULL load, not a boosted swap.
    await page.goto('/settings');
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('e2e: draft written on the sale screen');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(2);
    await expect(page.getByTestId('bugreport-toggle')).toHaveAttribute('aria-expanded', 'true');

    // …and again after a reload of that page: it is the session's draft.
    await page.reload();
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('e2e: draft written on the sale screen');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(2);
    assertClean();
  });

  test('draft survives a boosted rail navigation, chip stays in sync', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeAndroidScreenshot);
    await page.goto('/');
    const boot = await page.evaluate(() => (window as any).UT.shellBootAt as number);
    await page.getByTestId('bugreport-toggle').click();
    await page.locator('#ir-note').fill('e2e: boosted');
    await page.locator('#ir-screenshot-btn').click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);

    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    expect(await page.evaluate(() => (window as any).UT.shellBootAt as number)).toBe(boot);

    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('e2e: boosted');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);
    // The chip lives INSIDE the swapped region and is brand new after the
    // swap — it must still say the panel is open.
    await expect(page.getByTestId('bugreport-toggle')).toHaveAttribute('aria-expanded', 'true');
    assertClean();
  });

  test('a screen recording keeps running across a boosted navigation', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeDisplayMedia);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-btn')).toContainText('Stop');

    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    await expect(page.locator('#ir-screen-btn')).toContainText('Stop');
    await page.waitForTimeout(1500);
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-preview')).toBeVisible();
    const bytes = await page.evaluate(() => fetch((document.getElementById('ir-screen-preview') as HTMLVideoElement).src).then((r) => r.blob()).then((b) => b.size));
    expect(bytes).toBeGreaterThan(0);
    assertClean();
  });

  test('a screen recording interrupted by a full load is kept, not dropped', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeDisplayMedia);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-btn')).toContainText('Stop');
    // Let a few timeslice chunks land before the document is torn down.
    await page.waitForTimeout(2500);

    await page.goto('/settings');
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await expect(page.locator('#ir-screen-preview')).toBeVisible();
    await expect(page.locator('#ir-status')).toContainText('reloaded');
    await expect(page.locator('#ir-screen-btn')).not.toContainText('Stop');
    const bytes = await page.evaluate(() => fetch((document.getElementById('ir-screen-preview') as HTMLVideoElement).src).then((r) => r.blob()).then((b) => b.size));
    expect(bytes).toBeGreaterThan(0);

    // The restored recording travels with the draft from here on.
    await page.reload();
    await expect(page.locator('#ir-screen-preview')).toBeVisible();
    assertClean();
  });

  test('✕ hides the panel but keeps the draft; Discard drops it; Send clears it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeAndroidScreenshot);
    await page.goto('/');
    const panel = page.getByTestId('bugreport-panel');
    await page.getByTestId('bugreport-toggle').click();
    await page.locator('#ir-note').fill('e2e: kept behind the ✕');
    await page.locator('#ir-screenshot-btn').click();
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);

    await page.getByTestId('bugreport-close').click();
    await expect(panel).toBeHidden();
    await page.goto('/settings');
    await expect(panel).toBeHidden();
    await page.getByTestId('bugreport-toggle').click();
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('e2e: kept behind the ✕');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(1);

    // Discard asks, then drops everything and closes.
    page.once('dialog', (d) => d.accept());
    await page.getByTestId('bugreport-discard').click();
    await expect(panel).toBeHidden();
    await page.reload();
    await expect(panel).toBeHidden();
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.locator('#ir-note')).toHaveValue('');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(0);

    // Send: the report goes, the draft is cleared, the panel stays open
    // with the confirmation (it never closes on its own).
    await page.locator('#ir-note').fill('e2e: sent report');
    await page.locator('#ir-screenshot-btn').click();
    await page.locator('#ir-save-btn').click();
    await expect(page.locator('#ir-status')).toContainText('Saved');
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('');
    await expect(page.locator('#ir-screenshot-thumbs .bugreport-thumb')).toHaveCount(0);
    await page.reload();
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('');
    assertClean();
  });

  test('a panel opened by /report-issue stays open after typing + a full load (review finding 2)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/report-issue');
    const panel = page.getByTestId('bugreport-panel');
    await expect(panel).toBeVisible();
    // The first store write used to default `open` to false — as if ✕ had
    // been pressed — and the next full load hid a panel nobody closed.
    await page.locator('#ir-note').fill('e2e: typed into the server-opened panel');
    await page.goto('/report-issue');
    await expect(panel).toBeVisible();
    await expect(page.locator('#ir-note')).toHaveValue('e2e: typed into the server-opened panel');
    await page.goto('/settings');
    await expect(panel).toBeVisible();
    assertClean();
  });

  test('a newer interrupted recording wins over an older finished one (review finding 3)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(fakeDisplayMedia);
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    // First recording: start, wait, stop -> a finished blob in the store.
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-btn')).toContainText('Stop');
    await page.waitForTimeout(1500);
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-preview')).toBeVisible();
    const first = await page.evaluate(() => fetch((document.getElementById('ir-screen-preview') as HTMLVideoElement).src).then((r) => r.blob()).then((b) => b.size));
    // Second recording, interrupted by a full load after more footage.
    await page.locator('#ir-screen-btn').click();
    await expect(page.locator('#ir-screen-btn')).toContainText('Stop');
    await page.waitForTimeout(4500);
    await page.goto('/settings');
    await expect(page.locator('#ir-screen-preview')).toBeVisible();
    await expect(page.locator('#ir-status')).toContainText('reloaded');
    const restored = await page.evaluate(() => fetch((document.getElementById('ir-screen-preview') as HTMLVideoElement).src).then((r) => r.blob()).then((b) => b.size));
    expect(restored, 'the restored recording must be the interrupted (longer) one, not the old finished one').toBeGreaterThan(first);
    assertClean();
  });

  test('a restored dragged position is re-clamped when the viewport shrinks (review finding 4)', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    const head = page.locator('#bugreport-panel .bugreport-head');
    const box = (await head.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 - 100, box.y + box.height / 2 + 300, { steps: 5 });
    await page.mouse.up();

    await page.reload();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    await page.setViewportSize({ width: 1024, height: 400 });
    await expect.poll(async () => {
      const b = (await page.getByTestId('bugreport-panel').boundingBox())!;
      return b.y + b.height;
    }, { message: 'the panel must be pulled back inside a 400px-tall viewport' }).toBeLessThanOrEqual(401);
  });

  test('the dragged position is restored after a full load', async ({ page }) => {
    await page.goto('/');
    await page.getByTestId('bugreport-toggle').click();
    const head = page.locator('#bugreport-panel .bugreport-head');
    const box = (await head.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 - 200, box.y + box.height / 2 + 60, { steps: 5 });
    await page.mouse.up();
    const moved = (await page.getByTestId('bugreport-panel').boundingBox())!;

    await page.reload();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();
    const after = (await page.getByTestId('bugreport-panel').boundingBox())!;
    expect(Math.abs(after.x - moved.x)).toBeLessThan(2);
    expect(Math.abs(after.y - moved.y)).toBeLessThan(2);
  });
});
