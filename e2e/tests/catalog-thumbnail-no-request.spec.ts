import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#319: /catalog used to render EVERY row with an unconditional
// `<img src=".../thumb.png">`, so an item with no uploaded photo made the
// browser issue a real, doomed GET (404) on every single page view — hidden
// visually by `onerror`, but still a wasted request logged in the console.
// The seeded demo catalog ships a real thumb.png for every demo item, which
// is exactly why this stayed invisible: only an item created afterwards (by
// hand or by import), with no photo, exposes it. This drives a REAL browser
// against a freshly created photo-less item and proves — via the page's own
// network traffic, not a Go-level unit assertion — that no such request is
// ever made, while an item that DOES have a photo is unaffected.
test.describe('catalog thumbnails: no request for a missing photo (ut-docs#319)', () => {
  const ITEM_NAME = 'Thumbnail Regression Widget 319';
  let createdId: string | undefined;

  test.afterEach(async ({ page }) => {
    // Deactivate rather than leave it live for every later spec sharing this
    // till server (e2e/README.md's rule — same pattern as
    // catalog-import-friendly-errors.spec.ts's cleanup).
    if (!createdId) return;
    await page.request.post('/api/catalog/item/deactivate', { form: { id: createdId } });
    createdId = undefined;
  });

  test('an item with no uploaded photo triggers zero thumbnail requests, and renders a placeholder box', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);

    const created = await page.request.post('/api/catalog/item', {
      form: { name: ITEM_NAME, price: '199' },
    });
    expect(created.ok()).toBeTruthy();

    // Watch every request the page makes from the moment we navigate, so we
    // catch the thumbnail GET even though the <img>'s onerror hides its
    // failure visually — status codes/visibility prove nothing here, only
    // the absence of the request itself does.
    const thumbRequests: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/thumb.png')) thumbRequests.push(r.url());
    });

    await page.goto('/catalog');
    await expect(page.locator('body')).toContainText('Catalog');

    const row = page.locator('.catalog-row', { hasText: ITEM_NAME });
    await expect(row).toBeVisible();
    createdId = await row.getAttribute('data-id');
    expect(createdId).toBeTruthy();

    // No image element at all for this row — a CSS-only placeholder box
    // instead, so there was never a src for the browser to fetch.
    await expect(row.locator('img')).toHaveCount(0);
    await expect(row.locator('.catalog-thumb-cell .thumb')).toBeVisible();

    expect(
      thumbRequests.some((u) => u.includes(createdId!)),
      `expected no thumbnail request for the photo-less item, got: ${thumbRequests.join(', ')}`,
    ).toBe(false);

    // An existing item that DOES have a seeded photo must be unaffected —
    // still a real <img> pointed at its real thumbnail, still requested
    // normally (rows render `loading="lazy"`, so completeness/naturalWidth
    // depend on scroll position — src is what this change actually governs).
    const seededRow = page.locator('.catalog-row', { hasText: 'Sparkling Water 500ml' });
    await expect(seededRow.locator('img.thumb')).toHaveCount(1);
    await expect(seededRow.locator('img.thumb')).toHaveAttribute(
      'src',
      /\/public\/assets\/items\/itm003\/thumb\.png/,
    );
    await seededRow.locator('img.thumb').scrollIntoViewIfNeeded();
    await expect(seededRow.locator('img.thumb')).toHaveJSProperty('complete', true);
    await expect(seededRow.locator('img.thumb')).not.toHaveJSProperty('naturalWidth', 0);

    // The item-detail panel (web/ui/partials/catalog_variants.html) is a
    // SEPARATE surface on the same /catalog page with its own unconditional
    // item-thumb <img> — selecting the photo-less row must not fire a
    // thumbnail request there either.
    await row.click();
    await expect(page.locator('#catalog-variants')).toContainText(ITEM_NAME);
    await expect(page.locator('.catalog-detail-title img')).toHaveCount(0);
    await expect(page.locator('.catalog-detail-title .thumb')).toBeVisible();
    expect(
      thumbRequests.some((u) => u.includes(createdId!)),
      `expected no thumbnail request for the photo-less item's detail panel either, got: ${thumbRequests.join(', ')}`,
    ).toBe(false);

    assertClean();
  });
});
