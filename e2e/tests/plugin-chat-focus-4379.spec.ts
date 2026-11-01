import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#4379: the chat component (web/ui/partials/pluginview/controls.html,
// ut-docs#4132) re-renders into its target on every question -- the thinking
// bubble, then the answer -- which dropped focus from the question box, so
// the operator had to tap it again for a follow-up. app.js now returns focus
// to the box once the answer lands, only for a question the operator sent
// and only if they haven't moved to another control meanwhile. On a phone
// the transcript gets a shorter cap so the question row stays on screen.
//
// No e2e till carries a plugin with a chat view yet (ut-docs#4378), so this
// mounts the component's markup on a real till page (real htmx, real
// app.js, real app.css) and answers its requests with page.route, in the
// shapes the template draws: the thinking bubble polls the same target.

const ROUTE = '/__chat-4379';
const TARGET = 'pv-chat-4379';

function turns(n: number): string {
  let s = '';
  for (let i = 0; i < n; i++) {
    const role = i % 2 ? 'assistant' : 'user';
    s += `<div class="plugin-chat-turn ${role}"><p class="plugin-chat-text" dir="auto">Turn ${i} with enough words to wrap onto a second line on a phone</p></div>`;
  }
  return s;
}

function answer(n: number): string {
  return `<div class="card plugin-chat"><div class="plugin-chat-scroll"><div class="plugin-chat-log" role="log" aria-live="polite">${turns(n)}</div></div>
<form class="plugin-chat-ask" method="post" action="${ROUTE}" hx-post="${ROUTE}" hx-target="#${TARGET}" hx-swap="innerHTML" hx-disabled-elt="find button[type=submit]">
<input type="text" id="${TARGET}-chat-q" name="question" autocomplete="off" required>
<button type="submit" class="btn btn-touch primary">Send</button></form></div>`;
}

const thinking = `<div class="card plugin-chat"><div class="plugin-chat-scroll"><div class="plugin-chat-log" role="log" aria-live="polite">${turns(1)}
<div class="plugin-chat-turn assistant plugin-chat-thinking" hx-get="${ROUTE}?_job=1" hx-trigger="every 1s" hx-target="#${TARGET}" hx-swap="innerHTML">
<p class="plugin-chat-text">Thinking…</p></div></div></div></div>`;

// mount puts the component (html) on a real till page and wires the routes:
// a POST answers with the thinking bubble, its poll with the answer.
// hold, when given, delays the answer until it resolves; failPost answers the
// question the way plugin_view.go does for a failed action (#3879).
async function mount(page: Page, html: string, opts: { hold?: Promise<void>; failPost?: boolean } = {}) {
  await page.route(`**${ROUTE}*`, async (route) => {
    if (route.request().method() === 'POST') {
      if (opts.failPost) return route.fulfill({ status: 502, contentType: 'text/html', headers: { 'HX-Reswap': 'none' }, body: '' });
      return route.fulfill({ status: 200, contentType: 'text/html', body: thinking });
    }
    if (opts.hold) await opts.hold;
    return route.fulfill({ status: 200, contentType: 'text/html', body: answer(2) });
  });
  await page.goto('/reports');
  await page.evaluate(([id, inner]) => {
    const other = document.createElement('input');
    other.id = 'other-4379';
    other.setAttribute('aria-label', 'other');
    document.body.prepend(other);
    const el = document.createElement('div');
    el.id = id;
    el.innerHTML = inner;
    document.body.prepend(el);
    (window as unknown as { htmx: { process(e: Element): void } }).htmx.process(el);
  }, [TARGET, html]);
}

test.describe('plugin chat: focus and phone height (ut-docs#4379)', () => {
  test('after an answer, focus returns to the question box', async ({ page }) => {
    const assertClean = watchConsole(page);
    await mount(page, answer(1));
    const q = page.locator(`#${TARGET}-chat-q`);
    await q.fill('How much tea?');
    await q.press('Enter');
    await expect(page.locator(`#${TARGET} .plugin-chat-thinking`)).toBeVisible();
    await expect(page.locator(`#${TARGET} .plugin-chat-thinking`)).toHaveCount(0);
    await expect(page.locator(`#${TARGET}-chat-q`)).toBeFocused();
    await expect(page.locator(`#${TARGET}`)).not.toHaveAttribute('data-ut-chat-refocus', /.*/);
    assertClean();
  });

  test('a swap nobody asked a question for does not take focus', async ({ page }) => {
    const assertClean = watchConsole(page);
    // A page that opens on a running job: the poll swaps the answer in, but
    // the operator never sent a question from this box.
    await mount(page, thinking);
    await expect(page.locator(`#${TARGET} .plugin-chat-thinking`)).toHaveCount(0);
    await expect(page.locator(`#${TARGET}-chat-q`)).toBeVisible();
    await expect(page.locator(`#${TARGET}-chat-q`)).not.toBeFocused();
    assertClean();
  });

  test('an operator who moved to another control keeps it', async ({ page }) => {
    const assertClean = watchConsole(page);
    // The answer is held until the operator has moved on, so the refocus
    // branch is really exercised (not raced by the 1s poll).
    let release: () => void = () => {};
    const hold = new Promise<void>((r) => { release = r; });
    await mount(page, answer(1), { hold });
    await page.locator(`#${TARGET}-chat-q`).fill('How much tea?');
    await page.locator(`#${TARGET} button[type=submit]`).click();
    await expect(page.locator(`#${TARGET} .plugin-chat-thinking`)).toBeVisible();
    await page.locator('#other-4379').focus();
    release();
    await expect(page.locator(`#${TARGET} .plugin-chat-thinking`)).toHaveCount(0);
    await expect(page.locator('#other-4379')).toBeFocused();
    assertClean();
  });

  test('a failed question clears the mark and takes no focus', async ({ page }) => {
    await mount(page, answer(1), { failPost: true });
    const q = page.locator(`#${TARGET}-chat-q`);
    await q.fill('How much tea?');
    const done = page.waitForResponse((r) => r.url().includes(ROUTE) && r.status() === 502);
    await page.locator(`#${TARGET} button[type=submit]`).click();
    await done;
    await expect(page.locator(`#${TARGET}`)).not.toHaveAttribute('data-ut-chat-refocus', /.*/);
    await expect(q).not.toBeFocused();
  });

  test('on a 360px phone the transcript cap is shorter than on a tablet', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await mount(page, answer(30));
    const phone = await page.locator(`#${TARGET} .plugin-chat-scroll`).evaluate((e) => e.getBoundingClientRect().height);
    // min(40vh, 18rem) at 800px tall: 288px with a 16px root.
    expect(phone).toBeLessThanOrEqual(0.4 * 800 + 1);
    await page.setViewportSize({ width: 1024, height: 600 });
    const tablet = await page.locator(`#${TARGET} .plugin-chat-scroll`).evaluate((e) => e.getBoundingClientRect().height);
    // Unchanged above the phone breakpoint: min(60vh, 32rem) = 360px.
    expect(tablet).toBeGreaterThan(phone);
    expect(tablet).toBeLessThanOrEqual(0.6 * 600 + 1);
  });
});
