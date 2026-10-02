import { test, expect } from './fixtures';

// ut-docs#3356: on an iPhone, Settings -> Help: tapping a topic in the
// manual's tree did nothing visible — the stacked tree stayed pinned
// (sticky, viewport-tall) and the topic swapped in below it, out of sight.
// On a phone the topic replaces the tree, with a back link to restore it.
test.describe('phone', () => {
  test.use({ viewport: { width: 440, height: 956 }, hasTouch: true, isMobile: true });

  test('tapping a topic shows it in view; back restores the tree; search keeps its box', async ({ page }) => {
    await page.goto('/help');
    const link = page.locator('#manual-tree a.manual-link').nth(2);
    const href = await link.getAttribute('href');
    const id = href!.replace('/help/', '');
    await link.tap();

    const topic = page.locator(`#manual-topic[data-topic="${id}"]`);
    await expect(topic).toBeVisible();
    const h1 = topic.locator('h1').first();
    await expect(h1).toBeVisible();
    const box = await h1.boundingBox();
    expect(box!.y).toBeGreaterThanOrEqual(0);
    expect(box!.y).toBeLessThan(956);
    // The back link is what the user can actually see and tap — not tucked
    // under the fixed top bar.
    const hit = await page.locator('.manual-back').evaluate((el) => {
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return { onTop: !!top && el.contains(top), height: r.height };
    });
    expect(hit.onTop).toBe(true);
    expect(hit.height).toBeGreaterThanOrEqual(44);
    await expect(page.locator('#manual-tree')).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/help/${id}$`));

    await page.locator('.manual-back').tap();
    await expect(page.locator('#manual-tree')).toBeVisible();
    await expect(page.locator('.manual-back')).toHaveCount(0);

    await page.locator('#manual-q').fill('sale');
    const results = page.locator('.manual-results');
    await expect(results).toBeVisible();
    const rbox = await results.boundingBox();
    expect(rbox!.y).toBeGreaterThanOrEqual(0);
    expect(rbox!.y).toBeLessThan(956);
    await expect(page.locator('#manual-q')).toBeVisible();
    await expect(page.locator('#manual-tree')).toBeHidden();
  });

  // The tree is ~40 topics tall; a topic tapped from the bottom of it used
  // to land with the page still scrolled down there, so the heading was far
  // above the viewport. app.js scrolls back to the top after the swap.
  test('a topic tapped from deep in the tree still opens in view', async ({ page }) => {
    await page.goto('/help');
    const link = page.locator('#manual-tree a.manual-link').last();
    await link.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(500);
    const id = (await link.getAttribute('href'))!.replace('/help/', '');
    await link.tap();
    const h1 = page.locator(`#manual-topic[data-topic="${id}"] h1`).first();
    await expect(h1).toBeVisible();
    await expect.poll(async () => (await h1.boundingBox())!.y).toBeGreaterThanOrEqual(0);
    expect((await h1.boundingBox())!.y).toBeLessThan(956);
  });

  // U+2039 is bidi-mirrored: in RTL the chevron must sit at inline-start
  // (the right) and be drawn as-is — no extra CSS flip that would turn it
  // back towards inline-end.
  test('RTL: back chevron sits at inline-start and is not CSS-flipped', async ({ page }) => {
    await page.goto('/help?lang=fa');
    await page.locator('#manual-tree a.manual-link').nth(2).tap();
    const back = page.locator('.manual-back');
    await expect(back).toBeVisible();
    const g = await back.evaluate((el) => {
      const chev = el.querySelector('.manual-back-chev') as HTMLElement;
      const c = chev.getBoundingClientRect(), r = el.getBoundingClientRect();
      return { dir: getComputedStyle(el).direction, transform: getComputedStyle(chev).transform,
               chevRight: c.right, linkRight: r.right };
    });
    expect(g.dir).toBe('rtl');
    expect(g.transform).toBe('none');
    expect(g.linkRight - g.chevRight).toBeLessThan(20);
  });
});

test.describe('wide', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('tree stays visible beside the topic; no back link', async ({ page }) => {
    await page.goto('/help');
    const link = page.locator('#manual-tree a.manual-link').nth(2);
    const id = (await link.getAttribute('href'))!.replace('/help/', '');
    await link.click();
    await expect(page.locator(`#manual-topic[data-topic="${id}"]`)).toBeVisible();
    await expect(page.locator('#manual-tree')).toBeVisible();
    await expect(page.locator('.manual-back')).toBeHidden();
  });
});
