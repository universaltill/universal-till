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
