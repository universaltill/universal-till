import { test, expect, type Page } from './fixtures';

// ut-docs#5867: every actionable control in the status bar needs a >=44 CSS px
// hit area under `pointer: coarse` (WCAG 2.5.8 / the till's own touch floor)
// WITHOUT making the bar taller (#3459: a min-block-size chip grew the bar
// ~15px). The hit area is a centred ::before, so it is measured the way a
// finger meets it: elementFromPoint 21px above and below the control's centre
// line (and 21px to either side) must resolve to the control. Chips are the
// real markup (web/ui/layouts/base.html and web/ui/partials/*_chip.html),
// injected one at a time so the default till's state does not matter.

const CHIPS: Record<string, { html: string; target: string }> = {
  enrol: {
    html: `<a class="sb-item sb-enrol" href="/settings#registration">Register</a>`,
    target: '.sb-enrol',
  },
  'update-button': {
    html: `<button type="button" class="sb-item sb-update">Update</button>`,
    target: 'button.sb-update',
  },
  'update-link': {
    html: `<a class="sb-item sb-update" href="https://www.universaltill.com/download">Update</a>`,
    target: 'a.sb-update',
  },
  'update-stuck-link': {
    html: `<span class="sb-item sb-update is-stuck">Update v1 — <a data-testid="sb-update-stuck" href="/help/updates">blocked</a></span>`,
    target: '[data-testid=sb-update-stuck]',
  },
  power: {
    html: `<a class="sb-item sb-power" href="/help/power-supply">Power</a>`,
    target: '.sb-power',
  },
  'plugin-update': {
    html: `<a class="sb-item sb-plugin-update" href="/plugins" data-testid="sb-plugin-update">Plugin updates</a>`,
    target: '.sb-plugin-update',
  },
  'language-pack-update': {
    html: `<a class="sb-item sb-language-pack-update" href="/plugins">Language pack</a>`,
    target: '.sb-language-pack-update',
  },
  'subscription-link': {
    html: `<a class="sb-item sb-subscription is-stale" href="/settings#subscription">Subscription not confirmed</a>`,
    target: '.sb-subscription',
  },
  'subscription-button': {
    html: `<button type="button" class="sb-item sb-subscription is-stale">Subscription not confirmed</button>`,
    target: '.sb-subscription',
  },
  'main-till-link': {
    html: `<a class="sb-item sb-link is-linked" href="/tills"><span class="sb-dot"></span><span class="sb-link-text">Linked</span></a>`,
    target: '.sb-link',
  },
  'main-till-unreachable': {
    html: `<a class="sb-item sb-link is-unreachable sb-main-till" href="/tills">Main till unreachable</a>`,
    target: '.sb-main-till',
  },
  'cloud-auth': {
    html: `<a class="sb-item sb-cloud-auth" href="/settings#registration">Pairing needed</a>`,
    target: '.sb-cloud-auth',
  },
  'report-archive': {
    html: `<a class="sb-item sb-report-archive" href="/settings#settings-retention">Archive</a>`,
    target: '.sb-report-archive',
  },
  'disk-low': {
    html: `<a class="sb-item sb-disk-low" href="/settings#settings-backup">Disk low</a>`,
    target: '.sb-disk-low',
  },
  'shop-currency': {
    html: `<a class="sb-item sb-shop-currency" href="/settings#settings-currency">Currency</a>`,
    target: '.sb-shop-currency',
  },
  'backup-copy': {
    html: `<a class="sb-item sb-backup-copy" href="/settings#settings-backup">Back up</a>`,
    target: '.sb-backup-copy',
  },
  'release-notes-link': {
    html: `<span class="sb-item sb-release-notes"><a href="/settings#settings-about" data-testid="sb-release-notes-link">Updated to v1.0.0</a>` +
      `<button type="button" class="sb-release-notes-dismiss" data-testid="sb-release-notes-dismiss" aria-label="Dismiss"><span class="btn-ico" aria-hidden="true">x</span></button></span>`,
    target: '[data-testid=sb-release-notes-link]',
  },
  'release-notes-dismiss': {
    html: `<span class="sb-item sb-release-notes"><a href="/settings#settings-about">Updated to v1.0.0</a>` +
      `<button type="button" class="sb-release-notes-dismiss" data-testid="sb-release-notes-dismiss" aria-label="Dismiss"><span class="btn-ico" aria-hidden="true">x</span></button></span>`,
    target: '[data-testid=sb-release-notes-dismiss]',
  },
  'translate-off': {
    html: `<span class="sb-item sb-translate"><strong>Translate</strong><button type="button" class="sb-translate-off" data-testid="sb-translate-off">Turn off</button></span>`,
    target: '[data-testid=sb-translate-off]',
  },
};

async function inject(page: Page, html: string) {
  await page.evaluate((h) => {
    document.querySelector('.statusbar .sb-ver')!.insertAdjacentHTML('beforebegin', h);
  }, html);
}

async function barHeight(page: Page) {
  return page.evaluate(() => document.querySelector('.statusbar')!.getBoundingClientRect().height);
}

// Probe points around the control's centre; returns which of them reach it.
async function probe(page: Page, target: string) {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel) as HTMLElement;
    const b = el.getBoundingClientRect();
    const cx = b.left + b.width / 2;
    const cy = b.top + b.height / 2;
    const hit = (x: number, y: number) => {
      const h = document.elementFromPoint(x, y);
      return !!h && (h === el || el.contains(h));
    };
    return {
      up: hit(cx, cy - 21),
      down: hit(cx, Math.min(cy + 21, window.innerHeight - 1)),
      downClamped: cy + 21 > window.innerHeight - 1,
      left: hit(cx - 21, cy),
      right: hit(cx + 21, cy),
      h: b.height,
    };
  }, target);
}

const VIEWPORTS = [
  { name: 'kiosk 1024x600', width: 1024, height: 600 },
  { name: 'phone 360', width: 360, height: 800 },
];

for (const vp of VIEWPORTS) {
  test.describe(`coarse pointer, ${vp.name}`, () => {
    test.use({ viewport: { width: vp.width, height: vp.height }, hasTouch: true, isMobile: true });

    for (const [name, chip] of Object.entries(CHIPS)) {
      test(`${name}: >=44px hit area, bar does not grow`, async ({ page }) => {
        await page.goto('/settings?lang=en');
        expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
        await page.evaluate(() => {
          // Only the injected control may be on the bar.
          document.querySelectorAll('.statusbar > :not(#sb-conn):not(.sb-ver)').forEach((e) => e.remove());
        });
        await inject(page, chip.html);
        const el = page.locator(chip.target).first();
        await expect(el).toBeVisible();
        const after = await barHeight(page);
        // The hit area must add nothing to the bar: the same chip with every
        // statusbar ::before switched off lays the bar out identically.
        await page.addStyleTag({ content: '.statusbar *::before { display: none !important; }' });
        const without = await barHeight(page);
        expect(Math.abs(after - without), 'bar growth caused by the hit area').toBeLessThanOrEqual(0.5);
        await page.evaluate(() => document.querySelectorAll('style:last-of-type').forEach((e) => e.textContent?.includes('statusbar *::before') && e.remove()));
        const p = await probe(page, chip.target);
        expect(p.up, 'hit 21px above centre').toBe(true);
        expect(p.down || p.downClamped, 'hit 21px below centre').toBe(true);
        // The 36px-wide dismiss sits inside the release-notes pill: its inline
        // reach past 36px lies under that span's own box, which wins (z-index -1,
        // so a hit area never covers another box, ut-docs#5867 round 2).
        if (name !== 'release-notes-dismiss') {
          expect(p.left, 'hit 21px left of centre').toBe(true);
          expect(p.right, 'hit 21px right of centre').toBe(true);
        }
      });
    }

    test('every real visible a/button in .statusbar has a 44px hit area', async ({ page }) => {
      await page.goto('/settings?lang=en');
      const ctl = await page.evaluate(() =>
        Array.from(document.querySelectorAll('.statusbar a, .statusbar button'))
          .filter((e) => (e as HTMLElement).getClientRects().length > 0)
          .map((e, i) => { e.setAttribute('data-probe', String(i)); return i; }),
      );
      for (const i of ctl) {
        const p = await probe(page, `[data-probe="${i}"]`);
        expect(p.up && (p.down || p.downClamped) && p.left && p.right, `control ${i}`).toBe(true);
      }
    });
  });
}

// Several chips at once is the mix that makes the bar wrap (rows < 44px apart):
// a later chip's hit area must never sit over an earlier control's visible
// box, and no hit area may reach more than a few px above the bar (onto the
// page content). A 44px target in a ~25px bar cannot stay inside it (the bar
// is pinned to the screen bottom, nothing below), so the overshoot is the
// chip's own block overflow, ~6px; a clip on the bar would cut the target to
// bar height and is not used.
const MIXES = [['release-notes-link', 'update-button', 'power', 'enrol'], ['enrol', 'power', 'update-button', 'release-notes-link']];
const MIX_VIEWPORTS = [
  { name: 'phone 360x740', width: 360, height: 740 },
  { name: 'kiosk 1024x600', width: 1024, height: 600 },
];
for (const vp of MIX_VIEWPORTS) {
  for (const lang of ['en', 'fa']) for (const MIX of MIXES) {
    test.describe(`several chips at once, ${vp.name}, ${lang}, ${MIX[0]} first`, () => {
      test.use({ viewport: { width: vp.width, height: vp.height }, hasTouch: true, isMobile: true });

      test('controls own their whole box; hit areas stay inside the bar', async ({ page }) => {
        await page.goto(`/settings?lang=${lang}`);
        await page.evaluate(() => {
          document.querySelectorAll('.statusbar > :not(#sb-conn):not(.sb-ver)').forEach((e) => e.remove());
        });
        for (const m of MIX) await inject(page, CHIPS[m].html);
        const res = await page.evaluate(() => {
          const bar = document.querySelector('.statusbar') as HTMLElement;
          const bb = bar.getBoundingClientRect();
          const ctl = Array.from(bar.querySelectorAll('a, button')).filter((e) => (e as HTMLElement).getClientRects().length > 0) as HTMLElement[];
          const bad: string[] = [];
          for (const el of ctl) {
            const b = el.getBoundingClientRect();
            for (let i = 0; i < 5; i++) for (let j = 0; j < 5; j++) {
              // 5x5 grid over the border box, minus the corners outside the inscribed
              // ellipse (rounded pills/circles have no visible corner there).
              const u = (i - 2) * 0.45, v = (j - 2) * 0.45;
              if (u * u + v * v > 0.9) continue;
              const x = b.left + b.width / 2 + u * (b.width / 2);
              const y = b.top + b.height / 2 + v * (b.height / 2);
              const h = document.elementFromPoint(x, y);
              if (!h || !(h === el || el.contains(h))) {
                bad.push(`${el.className || el.tagName} @${Math.round(x)},${Math.round(y)} -> ${h && (h.className || h.tagName)}`);
              }
            }
          }
          const above: string[] = [];
          for (const el of ctl) {
            const b = el.getBoundingClientRect();
            const h = document.elementFromPoint(b.left + b.width / 2, bb.top - 9);
            if (h && bar.contains(h)) above.push(`${el.className || el.tagName} -> ${h.className || h.tagName}`);
          }
          return { n: ctl.length, bad, above };
        });
        expect(res.n).toBeGreaterThanOrEqual(MIX.length);
        expect(res.bad, 'points inside a control that hit something else').toEqual([]);
        expect(res.above, 'hit area reaching more than 8px above the bar').toEqual([]);
      });
    });
  }
}

test.describe('fine pointer (desktop) is untouched', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('no hit-area pseudo-element and the bar height does not change', async ({ page }) => {
    await page.goto('/settings?lang=en');
    expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(false);
    await page.evaluate(() => {
      document.querySelectorAll('.statusbar > :not(#sb-conn):not(.sb-ver)').forEach((e) => e.remove());
    });
    const bare = await barHeight(page);
    const heights: Record<string, number> = {};
    // Fine-pointer bar height with each chip showing, measured on main BEFORE
    // the change (1280x800, en); the change must not move any of them.
    const MAIN: Record<string, number> = {
      enrol: 34.95, 'update-button': 31.78, 'update-link': 32.95, 'update-stuck-link': 32.95, power: 32.95,
      'plugin-update': 32.95, 'language-pack-update': 32.95, 'subscription-link': 32.95, 'subscription-button': 32.95,
      'main-till-link': 42.25, 'main-till-unreachable': 42.25, 'cloud-auth': 32.95, 'report-archive': 32.95,
      'disk-low': 32.95, 'shop-currency': 32.95, 'backup-copy': 32.95, 'release-notes-link': 52.95,
      'release-notes-dismiss': 52.95, 'translate-off': 43.14,
    };
    for (const [name, chip] of Object.entries(CHIPS)) {
      await page.evaluate(() => document.querySelectorAll('[data-injected]').forEach((e) => e.remove()));
      await inject(page, chip.html.replace(/^<(\w+)/, '<$1 data-injected'));
      heights[name] = await barHeight(page);
      if (!name.startsWith('subscription')) {
        const content = await page.evaluate((sel) => getComputedStyle(document.querySelector(sel)!, '::before').content, chip.target);
        expect(content, `${name} ::before`).toBe('none');
      }
    }
    // Measured on main before this change (fine pointer, 1280x800).
    expect(bare).toBeGreaterThan(0);
    for (const [name, h] of Object.entries(heights)) {
      expect(h, `${name} bar height`).toBeCloseTo(MAIN[name], 0);
    }
  });
});
