import { test, expect } from './fixtures';

// ut-docs#5298: a destructive action whose request never gets an answer (the
// connection drops) may or may not have run. The Settings -> Data reset goes
// through window.utPostWithElevation; on a network failure it must show the
// translated "may not have completed" text in its own message slot -- not the
// raw "TypeError: Failed to fetch" -- re-enable the button, and send nothing
// again. The default project is auth-off (no PIN), so this is the first-send
// path. The request is aborted in the browser: nothing reaches the server, so
// nothing is wiped.

const OUTCOME = 'The connection dropped before the till replied, so this may or may not have been done. Check before you try again.';

test('reset-transactions network failure shows the outcome-unknown text, no retry', async ({ page }) => {
  let posts = 0;
  await page.route('**/api/data/reset-transactions', (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    return route.abort('failed');
  });
  await page.goto('/settings#settings-data');
  await page.locator('[data-testid=data-reset]').click();
  const text = page.locator('#data-reset-msg .pos-notice.error .notice-text');
  await expect(text).toHaveText(OUTCOME);
  const body = await page.locator('body').innerText();
  expect(body).not.toContain('TypeError');
  expect(body).not.toContain('Failed to fetch');
  await expect(page.locator('#data-reset-btn')).toBeEnabled();
  await expect(page.locator('#elevation-modal')).not.toHaveJSProperty('open', true);
  expect(posts).toBe(1);
});
