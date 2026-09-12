import { test, expect } from './fixtures';

// ut-docs#2141 regression pair -- see the "...-b-..." sibling file for the
// other half. Together they prove e2e/tests/fixtures.ts's
// `resetPosOncePerFile` auto-fixture actually DRAINS a held row an earlier
// spec FILE left behind, not just resets the basket -- the exact leak this
// card found in parked-orders-popup-2137.spec.ts's own two tests, and (per
// this card's own investigation, independent-review-corrected: two files
// originally listed here -- held-strip-scroll-affordance-2128.spec.ts and
// settings-osk.spec.ts -- were checked live and do NOT leak; the first
// already has its own afterEach cleanup, the second's hold dialog is
// cancelled, never submitted) in 5 other real spots across
// hold-named-tab.spec.ts, new-sale-closes-payment-overlay-1386.spec.ts,
// payment-overlay-duplicate-labels-1625.spec.ts,
// payment-overlay-footer-reachable-1542.spec.ts and
// tender-panel-reachable.spec.ts, none of which resume what they hold.
//
// This file deliberately plays that same "leaks a held row" role, rather
// than reusing one of the real files above, so this regression pair keeps
// working even after those files are eventually touched for unrelated
// reasons -- it doesn't depend on any of them keeping their current shape.
//
// File-sort order is a real, already-relied-on Playwright behavior in this
// suite (see playwright.config.ts's AUTH_ONLY_SPECS comment: login.spec.ts
// is verified via `--list` to run before nav-rail-lock-reachable-1346
// .spec.ts for exactly this reason), so naming this "...-a-..." and the
// sibling "...-b-..." pins the order deterministically.
test('deliberately parks a sale and does not resume it (ut-docs#2141 fixture regression, part a)', async ({
  page,
}) => {
  await page.goto('/');
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();

  // The modal hiding is NOT proof a row was actually held -- every
  // POST /api/pos/hold failure path (internal/pages/hold_api.go) still
  // answers 200 with an error toast, and the modal's own onclick closes on
  // any successful HTTP response regardless of the till's own verdict (an
  // independent review caught this: a probe with an EMPTY basket passes
  // the two assertions above with nothing ever parked). Assert the basket
  // actually cleared (parkASale in parked-orders-popup-2137.spec.ts does
  // the same) AND that a real held row exists server-side, so this test
  // fails loudly instead of silently if the park path itself ever breaks.
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
  const body = await (await page.request.get('/ui/parked-orders')).text();
  expect(body, 'a held row must actually exist for the sibling file to prove it gets drained').toMatch(
    /data-held-id="/,
  );

  // Deliberately no resume/drain here -- proving the NEXT file starts clean
  // anyway is the sibling file's job, not this one's.
});
