import { test, expect, type Page } from './fixtures';

// ut-docs#3459 (follow-up of #2569): the subscription chip in the status bar
// was capped at 16rem (272 px at the kiosk's 17 px root), and because
// `.statusbar` wraps (#3050) a long translation pushed the version label onto
// a second row (33 -> 56 px at 1024x600); its 2.25rem min height also made the
// whole bar ~15 px taller whenever it appeared. These specs force a long label
// through the real /ui/subscription-chip route and assert the geometry: one
// row, the text ellipsized, the 2.25rem touch target intact.

const LONG_LABEL =
  'Abonnement seit längerer Zeit nicht bestätigt: Bezahlfunktionen der Cloud pausieren bis zur nächsten Verbindung';

function chipHTML(label: string): string {
  // Same markup as web/ui/partials/subscription_chip.html.
  return (
    `<a class="sb-item sb-subscription is-stale" href="/settings#subscription" ` +
    `data-testid="sb-subscription" data-sub-state="stale" title="${label}">` +
    `<span class="sb-subscription-ico" aria-hidden="true">⚠</span>` +
    `<span class="sb-subscription-text">${label}</span></a>`
  );
}

async function routeChip(page: Page, label: string | null) {
  await page.route('**/ui/subscription-chip', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/html',
      body: label === null ? '' : chipHTML(label),
    }),
  );
}

async function barGeometry(page: Page) {
  return page.evaluate(() => {
    const r = (el: Element | null) => {
      const b = el!.getBoundingClientRect();
      return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, width: b.width, height: b.height };
    };
    const text = document.querySelector('.sb-subscription-text') as HTMLElement | null;
    return {
      bar: r(document.querySelector('.statusbar')),
      ver: r(document.querySelector('.statusbar .sb-ver')),
      chip: document.querySelector('.sb-subscription') ? r(document.querySelector('.sb-subscription')) : null,
      truncated: text ? text.scrollWidth > text.clientWidth : null,
      pageScrollsSideways: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    };
  });
}

test.describe('status-bar subscription chip stays on one row (ut-docs#3459)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('a very long label ellipsizes; the bar keeps one row and the version stays beside it', async ({ page }) => {
    await routeChip(page, null);
    await page.goto('/');
    await expect(page.locator('#sb-subscription-mount')).toBeAttached();
    const without = await barGeometry(page);

    await page.unroute('**/ui/subscription-chip');
    await routeChip(page, LONG_LABEL);
    await page.goto('/');
    await expect(page.locator('[data-testid=sb-subscription]')).toBeVisible();
    const withChip = await barGeometry(page);

    // One row: the version label sits on the chip's row, not below it.
    expect(Math.abs(withChip.ver.top - withChip.chip!.top)).toBeLessThanOrEqual(withChip.chip!.height);
    expect(withChip.ver.top).toBeLessThan(withChip.chip!.bottom);
    // The bar did not grow: neither a second row (+~23 px) nor the chip's own
    // tap-target height pushing the whole bar (and the page above) taller.
    expect(withChip.bar.height).toBeLessThanOrEqual(without.bar.height + 1);
    // The text is clipped with an ellipsis, the full label stays in the DOM.
    expect(withChip.truncated).toBe(true);
    await expect(page.locator('.sb-subscription-text')).toHaveText(LONG_LABEL);
    // Nothing spilled out of the bar or the page.
    expect(withChip.chip!.right).toBeLessThanOrEqual(withChip.bar.right + 0.5);
    expect(withChip.pageScrollsSideways).toBe(false);
  });

  test('the chip keeps a 2.25rem touch target (hit area) and sits inside the bar', async ({ page }) => {
    await routeChip(page, LONG_LABEL);
    await page.goto('/');
    await expect(page.locator('[data-testid=sb-subscription]')).toBeVisible();
    const g = await barGeometry(page);
    expect(g.chip!.top).toBeGreaterThanOrEqual(g.bar.top - 0.5);
    expect(g.chip!.bottom).toBeLessThanOrEqual(g.bar.bottom + 0.5);
    // Hit-test just inside 1.125rem above and below the chip's centre line:
    // a full 2.25rem target. The bar is pinned to the screen's bottom edge, so
    // below is capped there.
    const rem = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
    const cx = g.chip!.left + g.chip!.width / 2;
    const cy = g.chip!.top + g.chip!.height / 2;
    const reach = 1.125 * rem - 1;
    for (const dy of [-reach, Math.min(reach, 599 - cy)]) {
      const hit = await page.evaluate(
        ([x, y]) => !!document.elementFromPoint(x, y)?.closest('[data-testid=sb-subscription]'),
        [cx, cy + dy],
      );
      expect(hit, `hit at dy=${dy}`).toBe(true);
    }
  });

  // The card's scenario: a bar that has room for the chip at 14rem but not at
  // the old 16rem cap. Everything optional (update, register, marketplace...
  // whatever an earlier spec left on this worker's till) is hidden, then the
  // free gap is measured and a filler eats all of it but gap + margin + 15rem,
  // i.e. between the new and the old cap. Sized from the live layout so the
  // test does not depend on fonts or on the 17 px kiosk root.
  test('a bar with room for 14rem of chip but not 16rem keeps one row', async ({ page }) => {
    const stage = async () => {
      await page.evaluate(() => document.fonts.ready);
      await page.evaluate(() => {
        for (const el of Array.from(document.querySelectorAll('.statusbar > *'))) {
          const keep = el.id === 'sb-conn' || el.classList.contains('sb-ver') || el.id === 'sb-subscription-mount';
          if (!keep) (el as HTMLElement).style.display = 'none';
        }
      });
    };
    const measure = () =>
      page.evaluate(() => {
        const bar = document.querySelector('.statusbar') as HTMLElement;
        const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
        const gap = parseFloat(getComputedStyle(bar).columnGap);
        const ver = bar.querySelector('.sb-ver')!.getBoundingClientRect();
        const prevRight = Array.from(bar.children)
          .filter((c) => !c.classList.contains('sb-ver') && c.getBoundingClientRect().width > 0)
          .map((c) => c.getBoundingClientRect().right)
          .reduce((a, b) => Math.max(a, b), 0);
        // ver's auto margin absorbs the free space: its left edge sits one gap
        // plus all of it after the last item.
        return { rem, gap, free: ver.left - prevRight - gap };
      });
    const addFiller = (px: number) =>
      page.evaluate((w) => {
        document
          .querySelector('#sb-subscription-mount')!
          .insertAdjacentHTML('beforebegin', `<span class="sb-item" id="filler" style="inline-size:${w}px"></span>`);
      }, px);

    await routeChip(page, null);
    await page.goto('/');
    await expect(page.locator('#sb-subscription-mount')).toBeAttached();
    await stage();
    const { rem, gap, free } = await measure();
    const margin = 0.5 * rem; // .sb-subscription margin-inline-start
    // After the filler (one more gap) the chip needs gap + margin + its width.
    const filler = free - 2 * gap - margin - 15 * rem;
    expect(filler, 'the bar must have room to stage the scenario').toBeGreaterThan(0);
    await addFiller(filler);
    const without = await barGeometry(page);

    await page.unroute('**/ui/subscription-chip');
    await routeChip(page, LONG_LABEL);
    await page.goto('/');
    await expect(page.locator('[data-testid=sb-subscription]')).toBeVisible();
    await stage();
    await addFiller(filler);
    const g = await barGeometry(page);
    expect(g.chip!.width).toBeLessThanOrEqual(14 * rem + 0.5); // border-box: the cap is the whole chip
    // The chip is ~2px taller than the bare conn light; a wrapped row is ~20px.
    expect(g.bar.height).toBeLessThanOrEqual(without.bar.height + 4);
    expect(g.ver.top).toBeLessThan(g.chip!.bottom);
    expect(g.pageScrollsSideways).toBe(false);
  });

  for (const label of ['Subscription not confirmed', 'Subscription ended']) {
    test(`the shipped English label "${label}" is not truncated`, async ({ page }) => {
      await routeChip(page, label);
      await page.goto('/');
      await expect(page.locator('[data-testid=sb-subscription]')).toBeVisible();
      expect((await barGeometry(page)).truncated).toBe(false);
    });
  }
});

test.describe('below the kiosk width the bar still wraps (ut-docs#3050 / #413)', () => {
  test.use({ viewport: { width: 600, height: 900 } });

  test('a 600 px tablet: no sideways page scroll with the long chip', async ({ page }) => {
    await routeChip(page, LONG_LABEL);
    await page.goto('/');
    await expect(page.locator('[data-testid=sb-subscription]')).toBeVisible();
    expect((await barGeometry(page)).pageScrollsSideways).toBe(false);
  });
});
