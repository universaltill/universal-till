import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1295: two different, unrelated shops scanning the same LAN
// segment while both still on their unconfigured default name used to
// render as visually IDENTICAL discovery-list entries — the raw till_id
// UUID was the only thing distinguishing them, meaningless to a human
// without SSH/adb access to compare it by eye. This drives the real
// rendering path (not just a string assertion) with a mocked response
// carrying that exact collision, and checks the fix is actually visible:
// each entry's network address (derived client-side from base_url) is
// shown, and it differs between the two colliding candidates.
test('discovery list shows a distinguishing address when two candidates share a name', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.route('**/api/sync/discover-primaries', (route) =>
    route.fulfill({
      json: {
        data: {
          primaries: [
            { name: 'this shop', till_id: 'a1b2c3d4-e5f6-7890-abcd-ef1234567890', base_url: 'http://192.168.1.42:8080' },
            { name: 'this shop', till_id: '11111111-2222-3333-4444-555555555555', base_url: 'http://192.168.1.77:8080' },
          ],
        },
        error: null,
      },
    }),
  );

  await page.goto('/tills');

  const btn = page.locator('#discover-btn');
  await expect(btn).toBeEnabled();
  await btn.click();

  const items = page.locator('#discover-results li');
  await expect(items).toHaveCount(2);

  const texts = await items.allTextContents();
  // Both entries carry the same shop name (the actual collision) ...
  for (const t of texts) {
    expect(t).toContain('this shop');
  }
  // ... but each shows its own network address, and the two differ — the
  // concrete, human-legible disambiguator this card adds.
  expect(texts[0]).toContain('192.168.1.42');
  expect(texts[1]).toContain('192.168.1.77');
  expect(texts[0]).not.toBe(texts[1]);

  // Geometry sanity: both rows and their "Request to pair" hit targets are
  // actually visible, not collapsed/overlapping (the ut-docs#300 class of
  // defect this pipeline has shipped before without a geometric check).
  const buttons = page.locator('#discover-results button', { hasText: 'Request to pair' });
  await expect(buttons).toHaveCount(2);
  const [firstBox, secondBox] = await Promise.all([
    items.nth(0).boundingBox(),
    items.nth(1).boundingBox(),
  ]);
  expect(firstBox).toBeTruthy();
  expect(secondBox).toBeTruthy();
  if (firstBox && secondBox) {
    // The two rows must not overlap vertically.
    expect(firstBox.y + firstBox.height).toBeLessThanOrEqual(secondBox.y + 1);
  }

  assertClean();
});

// Independent review (ut-docs#1295): browse.go's entryAddr falls back to
// AddrV6 for a v6-only candidate (ut-docs#538), so an IPv6 base_url is a
// real production shape, not a hypothetical — and hostOf()'s regex fallback
// path (exercised here since jsdom-free Chromium still resolves `new URL()`
// for a bracketed IPv6 host, so this mainly guards against a future regex
// regression rather than the primary code path) must not truncate a
// bracketed IPv6 address at its first internal colon.
test('discovery list address handles an IPv6 candidate without truncating it', async ({ page }) => {
  const assertClean = watchConsole(page);

  await page.route('**/api/sync/discover-primaries', (route) =>
    route.fulfill({
      json: {
        data: {
          primaries: [
            { name: 'this shop', till_id: 'a1b2c3d4-e5f6-7890-abcd-ef1234567890', base_url: 'http://[2001:db8::1]:8080' },
          ],
        },
        error: null,
      },
    }),
  );

  await page.goto('/tills');
  await page.locator('#discover-btn').click();

  const item = page.locator('#discover-results li');
  await expect(item).toHaveCount(1);
  await expect(item).toContainText('2001:db8::1');

  assertClean();
});
