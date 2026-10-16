import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4370 (ADR-0160 §7): translate mode. A manager turns it on from
// the translation editor; every string then carries data-i18n-key, and
// Alt+click or a long press opens the key panel (.show(), not showModal)
// with the key's metadata, an Open in Weblate link + QR, a shop override
// and the page's areas. Driven at the 1024x600 kiosk floor. Never
// reachable on a self-order kiosk (the server refuses it there).
//
// Auth is off in the default project, so canPerform passes; the
// permission gate itself is covered by internal/pages translate_mode_test.

const WEBLATE = 'https://translate.universaltill.com/translate/universal-till/settings/en/?q=key%3A%3D%22translations.title%22';

test.use({ viewport: { width: 1024, height: 600 } });

async function turnOn(page: any) {
  await page.goto('/translations');
  await expect(page.getByTestId('translate-mode-card')).toBeVisible();
  await Promise.all([
    page.waitForEvent('load'),
    page.getByTestId('translate-mode-on').click(),
  ]);
  await expect(page.getByTestId('sb-translate-mode')).toBeVisible();
}

test('translate mode: strings carry their key, Alt+click opens the panel with a Weblate link', async ({ page }) => {
  const assertClean = watchConsole(page);
  await turnOn(page);

  const title = page.locator('h1[data-i18n-key~="translations.title"]');
  await expect(title).toBeVisible();
  // The invisible markers are gone from everything a person can see.
  const leftover = await page.evaluate(() => /\uDB40[\uDC00-\uDC7F]/.test(document.body.innerText + document.title));
  expect(leftover).toBe(false);
  // Attributes are tagged too (the search box's placeholder).
  await expect(page.locator('input[name="q"][data-i18n-key~="translations.search"]')).toHaveCount(1);

  await title.click({ modifiers: ['Alt'] });
  const panel = page.getByTestId('translate-panel');
  await expect(panel).toBeVisible();
  // Non-modal: the status bar's Turn off stays clickable behind it.
  expect(await panel.evaluate((d: HTMLDialogElement) => d.matches(':modal'))).toBe(false);
  await expect(page.getByTestId('translate-panel-key')).toHaveText('translations.title');
  await expect(page.getByTestId('translate-panel-area')).toHaveText('settings');
  const link = page.getByTestId('translate-panel-weblate');
  await expect(link).toHaveAttribute('href', WEBLATE);
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(page.getByTestId('translate-panel-qr')).toBeVisible();
  expect(await page.getByTestId('translate-panel-qr').evaluate((i: HTMLImageElement) => i.naturalWidth)).toBeGreaterThan(0);
  await expect(page.getByTestId('translate-panel-area-settings')).toBeVisible();
  await expect(title).toHaveClass(/ut-tm-selected/);

  // The panel fits the 1024x600 screen and clears the status bar.
  const box = await panel.boundingBox();
  const bar = await page.locator('.statusbar').boundingBox();
  expect(box && bar && box.y + box.height <= bar.y + 1).toBeTruthy();
  expect(box && box.x >= 0 && box.x + box.width <= 1024).toBeTruthy();
  await page.screenshot({ path: test.info().outputPath('translate-panel-1024x600.png') });

  await page.getByTestId('translate-panel-close').click();
  await expect(panel).toBeHidden();
  await expect(title).not.toHaveClass(/ut-tm-selected/);
  assertClean();
});

test('translate mode: a long press on touch opens the panel; a short tap does not', async ({ page }) => {
  await turnOn(page);
  const title = page.locator('h1[data-i18n-key~="translations.title"]');
  const press = (ms: number) => title.evaluate(async (el: HTMLElement, holdMs: number) => {
    const r = el.getBoundingClientRect();
    const o = { pointerType: 'touch', isPrimary: true, bubbles: true, clientX: r.x + 5, clientY: r.y + 5 };
    el.dispatchEvent(new PointerEvent('pointerdown', o));
    await new Promise((res) => setTimeout(res, holdMs));
    el.dispatchEvent(new PointerEvent('pointerup', o));
  }, ms);
  await press(150);
  await page.waitForTimeout(700);
  await expect(page.getByTestId('translate-panel')).toBeHidden();
  await press(800);
  await expect(page.getByTestId('translate-panel')).toBeVisible();
  await expect(page.getByTestId('translate-panel-key')).toHaveText('translations.title');
});

test('translate mode: shop override from the panel, then reset', async ({ page }) => {
  await turnOn(page);
  await page.locator('h1[data-i18n-key~="translations.title"]').click({ modifiers: ['Alt'] });
  await expect(page.getByTestId('translate-panel-key')).toHaveText('translations.title');
  await page.locator('#translate-panel-value').fill('Wording editor');
  await page.getByTestId('translate-panel-save').click();
  await expect(page.getByTestId('translate-panel-saved')).toBeVisible();
  try {
    await page.reload();
    await expect(page.locator('h1')).toHaveText('Wording editor');
  } finally {
    await page.locator('h1[data-i18n-key~="translations.title"]').click({ modifiers: ['Alt'] });
    await page.getByTestId('translate-panel-reset').click();
    await expect(page.getByTestId('translate-panel-saved')).toBeVisible();
  }
  await page.reload();
  await expect(page.locator('h1')).toHaveText('Translations');
});

test('translate mode: Turn off in the status bar returns plain pages', async ({ page }) => {
  await turnOn(page);
  await Promise.all([
    page.waitForEvent('load'),
    page.getByTestId('sb-translate-off').click(),
  ]);
  await expect(page.getByTestId('sb-translate-mode')).toHaveCount(0);
  await expect(page.locator('[data-i18n-key]')).toHaveCount(0);
  await expect(page.getByTestId('translate-panel')).toHaveCount(0);
});

test('translate mode: phone width keeps the panel on screen', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await turnOn(page);
  await page.locator('h1[data-i18n-key~="translations.title"]').click({ modifiers: ['Alt'] });
  const panel = page.getByTestId('translate-panel');
  await expect(page.getByTestId('translate-panel-key')).toHaveText('translations.title');
  const box = await panel.boundingBox();
  expect(box && box.x >= 0 && box.x + box.width <= 360).toBeTruthy();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(overflow).toBe(false);
  await page.screenshot({ path: test.info().outputPath('translate-panel-360.png') });
});

test('translate mode: unreachable on a self-order kiosk', async ({ page }) => {
  const set = (mode: string) => page.request.post('/api/settings/display-mode', { form: { mode } });
  const r = await set('self_order');
  expect(r.ok(), await r.text()).toBe(true);
  try {
    const on = await page.request.post('/api/translate-mode', { form: { on: '1' } });
    expect(on.status()).toBe(403);
    expect(on.headers()['set-cookie'] ?? '').not.toContain('ut_translate_mode=1');
    // Even a cookie set earlier (before the till became a kiosk) is ignored.
    const panel = await page.request.get('/ui/translate-mode/panel?key=common.close', { headers: { Cookie: 'ut_translate_mode=1' } });
    expect(panel.status()).toBe(403);
    const page2 = await page.request.get('/translations', { headers: { Cookie: 'ut_translate_mode=1' } });
    expect(await page2.text()).not.toContain('translate-mode.js');
    const editor = await page.request.get('/translations');
    expect(await editor.text()).not.toContain('data-testid="translate-mode-card"');
  } finally {
    const back = await set('register');
    expect(back.ok(), await back.text()).toBe(true);
  }
});

// Review M1: Chrome on Android fires its own contextmenu at ~500 ms of a
// touch hold (before the 600 ms long press) and then pointercancel. The
// contextmenu must be prevented and the panel must still open.
test('translate mode: Android long press (contextmenu at 500 ms, then pointercancel) opens the panel', async ({ page }) => {
  await turnOn(page);
  const title = page.locator('h1[data-i18n-key~="translations.title"]');
  const prevented = await title.evaluate(async (el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    const o = { pointerType: 'touch', isPrimary: true, bubbles: true, cancelable: true, clientX: r.x + 5, clientY: r.y + 5 };
    el.dispatchEvent(new PointerEvent('pointerdown', o));
    await new Promise((res) => setTimeout(res, 500));
    const cm = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: o.clientX, clientY: o.clientY });
    el.dispatchEvent(cm);
    el.dispatchEvent(new PointerEvent('pointercancel', o));
    return cm.defaultPrevented;
  });
  expect(prevented).toBe(true);
  await expect(page.getByTestId('translate-panel')).toBeVisible();
  await expect(page.getByTestId('translate-panel-key')).toHaveText('translations.title');
  // The text under the finger is not selectable (no Android selection handles).
  expect(await title.evaluate((el) => getComputedStyle(el).userSelect)).toBe('none');
  // A mouse right-click (no press in progress) keeps the browser's menu.
  await page.getByTestId('translate-panel-close').click();
  const rightClick = await title.evaluate((el: HTMLElement) => {
    const cm = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    el.dispatchEvent(cm);
    return cm.defaultPrevented;
  });
  expect(rightClick).toBe(false);
});

// Review M2: on the sale screen a ~500 ms hold on a quick button arms its
// edit (jiggle) mode. In translate mode the long press is the key panel's,
// so the hold (and Android's contextmenu) must not arm edit mode too. A
// control run first proves the injected tile does arm it outside the mode.
test('translate mode: a long press on a sale tile does not arm quick-button edit mode', async ({ page }) => {
  const hold = async () => {
    await page.goto('/');
    await expect(page.locator('#buttons-grid')).toHaveCount(1);
    return page.evaluate(async () => {
      const grid = document.getElementById('buttons-grid')!;
      const tile = document.createElement('button');
      tile.type = 'button';
      tile.className = 'btn-tile';
      tile.setAttribute('data-code', 'TM4370-TEST');
      tile.setAttribute('data-i18n-key', 'common.close');
      tile.textContent = 'TM4370';
      grid.appendChild(tile);
      const r = tile.getBoundingClientRect();
      const o = { pointerType: 'touch', isPrimary: true, bubbles: true, cancelable: true, button: 0, pointerId: 7, clientX: r.x + 2, clientY: r.y + 2 };
      tile.dispatchEvent(new PointerEvent('pointerdown', o));
      await new Promise((res) => setTimeout(res, 520));
      tile.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }));
      await new Promise((res) => setTimeout(res, 150));
      const armed = grid.classList.contains('jiggle-mode');
      tile.dispatchEvent(new PointerEvent('pointerup', o));
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      return armed;
    });
  };
  expect(await hold()).toBe(true); // control: outside translate mode the hold arms edit mode
  await turnOn(page);
  expect(await hold()).toBe(false);
  await expect(page.getByTestId('translate-panel')).toBeVisible();
  await expect(page.getByTestId('translate-panel-key')).toHaveText('common.close');
});

// Review L3: only a marker's own tag characters are removed; the England,
// Scotland and Wales flags (tag sequences) in a translated string survive.
test('translate mode: flag emoji tag sequences survive the marker sweep', async ({ page }) => {
  await turnOn(page);
  const res = await page.evaluate(async () => {
    const tag = (s: string) => Array.from(s).map((c) => String.fromCodePoint(0xE0000 + c.charCodeAt(0))).join('');
    const flag = (sub: string) => '\u{1F3F4}' + tag(sub) + '\u{E007F}';
    const text = flag('gbeng') + ' ' + flag('gbsct') + ' ' + flag('gbwls') + ' Close';
    const marked = text + '\u{E0001}' + tag('common.close') + '\u{E007F}';
    const p = document.createElement('p');
    p.textContent = marked;
    document.querySelector('main, body')!.appendChild(p);
    await new Promise((r) => setTimeout(r, 50));
    return { got: p.textContent, want: text, key: p.getAttribute('data-i18n-key') };
  });
  expect(res.key).toBe('common.close');
  expect(res.got).toBe(res.want);
});
