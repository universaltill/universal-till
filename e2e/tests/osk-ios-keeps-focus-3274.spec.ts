import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setOskMode, openNewItemForm } from './helpers';

// ut-docs#3274: on iPhone/iPad (WKWebView) the on-screen keyboard closed
// after every letter. osk.js keeps focus in the field by cancelling each
// key's pointerdown, which Chromium and WebKitGTK honour — but iOS WebKit
// moves focus from its tap gesture instead, AFTER pointerup (touchend), so
// the field blurred, focusout hid the keyboard, and the operator had to tap
// the field again for every character.
//
// No iOS engine runs in CI, so this drives iOS's event order by hand on a
// key: pointerdown, pointerup (the key's own activation), then the tap's
// late focus change — a cancelable mousedown whose default action (when
// nothing cancels it) is moving focus off the field, and, for the case
// where WebKit moves focus without dispatching mousedown at all, a bare
// blur of the field.

async function iosTapKey(page: Page, k: string, opts: { withMousedown: boolean }) {
  return page.evaluate(({ k, withMousedown }) => {
    const btn = document.querySelector(`#osk button[data-k="${k}"]`) as HTMLElement;
    const r = btn.getBoundingClientRect();
    const at = { bubbles: true, cancelable: true, composed: true,
      clientX: r.left + r.width / 2, clientY: r.top + r.height / 2 };
    const pe = { ...at, pointerId: 7, pointerType: 'touch', isPrimary: true };
    btn.dispatchEvent(new PointerEvent('pointerdown', pe));
    btn.dispatchEvent(new PointerEvent('pointerup', pe));
    let prevented = false;
    if (withMousedown) {
      prevented = !btn.dispatchEvent(new MouseEvent('mousedown', at));
    }
    // The tap's default focus change: iOS buttons are not focusable, so
    // focus falls to <body>.
    if (!prevented) (document.activeElement as HTMLElement | null)?.blur();
    return prevented;
  }, { k, withMousedown: opts.withMousedown });
}

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('a mousedown on an OSK key is cancelled, so the tap cannot move focus off the field', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');
  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  for (const ch of 'tea') {
    expect(await iosTapKey(page, ch, { withMousedown: true })).toBe(true);
  }
  await expect(name).toHaveValue('tea');
  await expect(name).toBeFocused();
  await expect(page.locator('#osk')).toBeVisible();
  assertClean();
});

test('focus knocked off the field by an OSK key tap comes back: the keyboard stays open for a whole word', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');
  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();

  for (const ch of 'coffee') {
    await iosTapKey(page, ch, { withMousedown: false });
    // Past focusout's 50ms settle window: a hide would have happened.
    await page.waitForTimeout(120);
    await expect(page.locator('#osk')).toBeVisible();
    await expect(name).toBeFocused();
  }
  await expect(name).toHaveValue('coffee');
  // The caret stayed at the end: the next real key appends.
  await page.locator('#osk button[data-k="s"]').click();
  await expect(name).toHaveValue('coffees');
  assertClean();
});

test('a tap outside the keyboard right after a key still closes it (only key taps keep focus)', async ({ page }) => {
  await setOskMode(page, 'on');
  await page.goto('/catalog');
  await openNewItemForm(page);
  const name = page.locator('#item-name');
  await name.click();
  await expect(page.locator('#osk')).toBeVisible();
  await iosTapKey(page, 'a', { withMousedown: false });
  await page.waitForTimeout(120);
  await expect(page.locator('#osk')).toBeVisible();
  // iOS order again, well inside the keep-focus window, but on a
  // non-focusable element outside #osk: its pointerdown, then the tap's
  // late blur of the field (focus to <body>).
  await page.evaluate(() => {
    const el = document.querySelector('footer.statusbar') as HTMLElement;
    const pe = { bubbles: true, cancelable: true, composed: true, pointerId: 8, pointerType: 'touch', isPrimary: true };
    el.dispatchEvent(new PointerEvent('pointerdown', pe));
    el.dispatchEvent(new PointerEvent('pointerup', pe));
    (document.activeElement as HTMLElement | null)?.blur();
  });
  await expect(page.locator('#osk')).toBeHidden();
  await expect(name).not.toBeFocused();
});
