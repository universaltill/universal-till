import { Page, expect } from '@playwright/test';

// Fail any spec that triggers a JS error — the class of bug our server-side
// tests can never see (the PIN-in-header regression, dead buttons, …).
export function watchConsole(page: Page): () => void {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text());
  });
  return () => expect(errors, 'page produced console errors').toEqual([]);
}

// The OSK mode is a SERVER-side setting shared by every spec on this server
// — callers must restore 'auto' in an afterEach even when the test body
// fails, or a failed run leaks e.g. osk=on into unrelated later specs.
export async function setOskMode(page: Page, mode: string) {
  await page.goto('/settings');
  const osk = page.locator('form[hx-post="/api/settings/osk"] select');
  await osk.selectOption(mode);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/osk')),
    osk.locator('..').locator('button[type=submit]').click(),
  ]);
  await page.waitForEvent('load');
}
