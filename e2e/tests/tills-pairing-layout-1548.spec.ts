import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#1548: three defects reported from the pilot pair, all reproduced
// and measured on the real tablet — /tills overflowed the viewport by 40-
// 59px (pending-pairings table + a wide manager-PIN input), the till-name
// field's `required` attribute was invisible until submit, and "find on
// this network" / "paste a pairing code" were two stacked sections each
// with their own till-name field. This suite proves the three fixes
// directly, the same way ut-docs#413's phone-width suite measures
// scrollWidth vs clientWidth rather than trusting the CSS by eye.

// Any 64-character commitment satisfies POST /api/sync/pair-request's
// validation (it only checks the length — the real cryptographic use is on
// the replica's own claim call, which this layout suite never reaches).
const SEED_COMMITMENT = 'a'.repeat(64);

// A realistic LAN device name: `device_name` is untrusted, unbounded input
// from whatever box is asking to pair, and it is what the row's PIN
// placeholder/aria-label embed — i.e. the widest thing in the table.
const SEED_DEVICE = 'Kitchen Counter Till (Back of House)';

test.describe('tills page pairing layout (ut-docs#1548)', () => {
  for (const vp of [
    { width: 1280, height: 800 },
    { width: 1024, height: 600 },
  ]) {
    test(`/tills never needs horizontal scroll at ${vp.width}x${vp.height}`, async ({ page, request }) => {
      // Seed a REAL pending request first. Measured during review: with an
      // empty pending list (the state this spec used to load) /tills does
      // not overflow at either viewport even with the entire AC-1 fix
      // deleted — the .pairing-table rules could be removed from app.css
      // and this test still passed. The 40-59px the pilot reported comes
      // from the pending-pairings TABLE, which only exists when something
      // is actually pending, so the table has to be on screen for this
      // assertion to mean anything. Re-measured with the fix reverted and
      // one seeded request: 1121 vs 1024 = 97px of real overflow at
      // 1024x600, which is what this now guards.
      const seeded = await request.post('/api/sync/pair-request', {
        data: { device_name: SEED_DEVICE, commitment: SEED_COMMITMENT },
      });
      expect(seeded.status(), 'seeding a pending pair request').toBe(200);
      const seededID = (await seeded.json()).data.id;

      try {
        await page.setViewportSize(vp);
        const assertClean = watchConsole(page);
        await page.goto('/tills');
        await page.waitForSelector('.tills-join');
        // Fail loudly rather than silently measuring the empty state again
        // if the partial, its poll, or the seed endpoint ever regresses.
        await expect(page.locator('.pairing-table')).toHaveCount(1);
        await expect(page.locator('input[name="manager_pin"]')).toHaveCount(1);

        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }));
        expect(
          overflow.scrollWidth,
          `document is ${overflow.scrollWidth - overflow.clientWidth}px wider than the viewport ` +
            `(scrollWidth=${overflow.scrollWidth}, clientWidth=${overflow.clientWidth})`,
        ).toBeLessThanOrEqual(overflow.clientWidth);

        assertClean();
      } finally {
        // Every spec in this project shares ONE till server (fixtures.ts),
        // and the basket-only reset fixture does not clear pairing rows —
        // so put the queue back the way we found it even on failure.
        await request.post(`/api/sync/pair-requests/${seededID}/deny`);
      }
    });
  }

  test('discover and paste-a-code are two tabs sharing one till-name field, not two stacked sections', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/tills');
    await page.waitForSelector('.tills-join');

    // Exactly one shared till-name field for the whole join component —
    // before this fix there were two (one per stacked section).
    await expect(page.locator('#pairing-till-name')).toHaveCount(1);

    const discoverTab = page.locator('#tills-tab-discover');
    const codeTab = page.locator('#tills-tab-code');
    const discoverPanel = page.locator('#tills-panel-discover');
    const codePanel = page.locator('#tills-panel-code');

    // Discover tab is the default.
    await expect(discoverTab).toHaveAttribute('aria-selected', 'true');
    await expect(discoverPanel).toBeVisible();
    await expect(codePanel).toBeHidden();

    await codeTab.click();
    await expect(codeTab).toHaveAttribute('aria-selected', 'true');
    await expect(discoverTab).toHaveAttribute('aria-selected', 'false');
    await expect(codePanel).toBeVisible();
    await expect(discoverPanel).toBeHidden();
    // The code tab's own form no longer carries a second till-name input.
    await expect(codePanel.locator('input[name="name"]')).toHaveCount(0);

    await discoverTab.click();
    await expect(discoverPanel).toBeVisible();
    await expect(codePanel).toBeHidden();

    assertClean();
  });

  test('an empty required till-name field shows a persistent red border + message, not just a submit-time bubble', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/tills');
    await page.waitForSelector('.tills-join');

    const nameInput = page.locator('#pairing-till-name');
    const error = page.locator('#pairing-till-name-error');
    await expect(error).toBeHidden();

    // The blocked submit must not actually reach the server.
    let joinPosts = 0;
    await page.route('**/api/sync/join', async (route) => {
      joinPosts += 1;
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<span>stubbed</span>' });
    });

    // Switch to the code tab and try to submit with the shared name field
    // still empty — reportValidity() fires the browser's native `invalid`
    // event, which this page's own script mirrors into a persistent
    // visual state (native bubbles alone are transient and easy to miss
    // on a touchscreen till).
    await page.locator('#tills-tab-code').click();
    await page.locator('input[name="code"]').fill('some-code');
    await page.locator('#tills-panel-code button[type="submit"]').click();

    await expect(error).toBeVisible();
    await expect(nameInput).toHaveClass(/field-invalid/);
    expect(joinPosts, 'a blocked submit must not reach /api/sync/join').toBe(0);

    // Filling it in clears the persistent state again.
    await nameInput.fill('Till 2');
    await expect(error).toBeHidden();
    await expect(nameInput).not.toHaveClass(/field-invalid/);

    // ...and the form still WORKS after having been blocked once. The guard
    // cancels htmx:beforeRequest; htmx 1.9.12 applies hx-disabled-elt and
    // the indicator only AFTER that event, so a cancel must not leave the
    // submit button permanently disabled. Assert that rather than trusting
    // the minified source's ordering.
    await page.locator('#tills-panel-code button[type="submit"]').click();
    await expect.poll(() => joinPosts, { timeout: 5000 }).toBe(1);
    await expect(page.locator('#tills-panel-code button[type="submit"]')).toBeEnabled();

    assertClean();
  });
});
