import { test, expect } from '@playwright/test';
import { watchConsole, ensureOperator } from './helpers';

// ut-docs#1170: the operator reported the floor-plan editor's table-drag
// gesture doesn't work by touch at all ("in the table design ... cannot
// move tables"). The drag-to-place code (web/ui/pages/tables.html,
// ADR-0054/ut-docs#814) already used unified Pointer Events (touch + mouse
// share one pointerdown/pointermove/pointerup path with preventDefault() +
// setPointerCapture()) before this card, and ut-docs#1025 already scoped
// `touch-action: none` to edit mode so the browser's own pan gesture
// doesn't compete with an active drag — but neither of those had ANY e2e
// coverage of the drag gesture itself: tables-keyboard-reposition-826.spec.ts
// only covers the keyboard-nudge path, and tables-tap-to-add-1025.spec.ts
// only asserts the touch-action CSS scoping, not that a drag actually moves
// and persists a table. This fills that gap.
//
// HONESTY NOTE: this dispatches a synthetic PointerEvent sequence with
// pointerType: 'touch' via Playwright's dispatchEvent — it exercises the
// same JS code path a real touchscreen drag would (pointerdown -> pointermove
// -> pointerup, targeting the .table-node the way the real gesture does),
// but it is not real touch hardware and cannot rule out an OS/compositor-
// level event-delivery problem. That broader question stays tracked under
// ut-docs#1021 (blocked:env, pending real Pi + DevTools reproduction) and is
// out of scope here — this test only proves the app's own drag handling
// still works when a touch pointer sequence actually reaches it.

async function deactivateAllTables(page) {
  await ensureOperator(page);
  await page.goto('/tables');
  for (;;) {
    const btn = page.locator('form[action$="/active"] button', { hasText: 'Deactivate' }).first();
    if ((await btn.count()) === 0) break;
    await Promise.all([page.waitForURL((u) => u.pathname === '/tables'), btn.click()]);
  }
}

async function createTableViaCard(page, label: string) {
  await page.locator('.users-form form[action="/api/tables"] input[name="label"]').fill(label);
  await Promise.all([
    page.waitForURL((u) => u.pathname === '/tables'),
    page.locator('.users-form form[action="/api/tables"] button[type=submit]').click(),
  ]);
}

test('dragging a table by touch moves and persists its position (ut-docs#1170)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await deactivateAllTables(page);
  await createTableViaCard(page, 'E2E Touch Drag 1170');

  await page.locator('#tables-edit-toggle').click();
  const node = page.locator('.table-node', { hasText: 'E2E Touch Drag 1170' });
  await expect(node).toBeVisible();

  const startX = Number(await node.getAttribute('data-x'));
  const startY = Number(await node.getAttribute('data-y'));

  const box = await node.boundingBox();
  expect(box).not.toBeNull();
  const originX = box!.x + box!.width / 2;
  const originY = box!.y + box!.height / 2;
  // A real, visible drag distance in screen pixels — the handler converts
  // through the SVG's viewBox scale, so this need not (and shouldn't) equal
  // the resulting canvas-unit delta exactly; the assertions below check the
  // node actually moved and the move persisted, not a specific offset.
  const destX = originX + 90;
  const destY = originY + 70;

  await node.dispatchEvent('pointerdown', {
    pointerId: 1,
    pointerType: 'touch',
    isPrimary: true,
    button: 0,
    clientX: originX,
    clientY: originY,
  });
  // The move/up listeners are on `document`, not the node itself (the whole
  // <svg> is replaced on every HTMX refresh — see the handler's own
  // comment) — dispatch there so they actually receive the events.
  await page.dispatchEvent('body', 'pointermove', {
    pointerId: 1,
    pointerType: 'touch',
    clientX: destX,
    clientY: destY,
  });

  const posResponse = page.waitForResponse((r) => r.url().includes('/position') && r.request().method() === 'POST');
  await page.dispatchEvent('body', 'pointerup', {
    pointerId: 1,
    pointerType: 'touch',
    clientX: destX,
    clientY: destY,
  });
  const res = await posResponse;
  expect(res.ok()).toBe(true);

  const movedX = Number(await node.getAttribute('data-x'));
  const movedY = Number(await node.getAttribute('data-y'));
  expect(movedX !== startX || movedY !== startY, 'table did not move at all').toBe(true);

  // Reload and confirm the move actually persisted server-side, not just
  // client-side DOM state.
  await page.reload();
  const reloaded = page.locator('.table-node', { hasText: 'E2E Touch Drag 1170' });
  expect(Number(await reloaded.getAttribute('data-x'))).toBe(movedX);
  expect(Number(await reloaded.getAttribute('data-y'))).toBe(movedY);

  assertClean();
});
