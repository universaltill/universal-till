import { Page, expect } from '@playwright/test';

// ---------------------------------------------------------------------------
// Form-layout geometry (ut-docs#300). Shared because the two surfaces that had
// the inline-label defect live in different e2e projects: the payout dialog is
// on the default (auth-off) till, and the change-PIN page needs a real
// authenticated operator, which only the auth project's login flow ever has.
// ---------------------------------------------------------------------------

export type FieldGeometry = {
  name: string;
  /** The wrapping <label>'s own class list, so a caller can select the rows it
   *  means (e.g. `.set-row`) rather than guessing from the control's name. */
  labelClass: string;
  text: { top: number; bottom: number; left: number; right: number; width: number };
  control: { top: number; bottom: number; left: number; right: number; width: number; height: number };
  label: { left: number; right: number; width: number };
  hit: boolean;
};

// Measures, for every <label> under `selector` that wraps a control: the rect
// of the label's OWN text (a Range over its text nodes, so the wrapped
// control's box is excluded), the control's rect, and whether the control is
// the real hit-test target at its own centre.
export async function fieldGeometry(page: Page, selector: string): Promise<FieldGeometry[]> {
  return page.evaluate((sel) => {
    const root = document.querySelector(sel);
    if (!root) throw new Error(`no element matched ${sel}`);
    return Array.from(root.querySelectorAll('label'))
      .map((l) => {
        const control = l.querySelector('input, select, textarea') as HTMLElement | null;
        if (!control) return null;
        const textNodes = Array.from(l.childNodes).filter(
          (n) => n.nodeType === Node.TEXT_NODE && (n.textContent || '').trim() !== '',
        );
        if (textNodes.length === 0) return null;
        const range = document.createRange();
        range.setStart(textNodes[0], 0);
        const last = textNodes[textNodes.length - 1];
        range.setEnd(last, (last.textContent || '').length);
        const t = range.getBoundingClientRect();
        const c = control.getBoundingClientRect();
        const lr = l.getBoundingClientRect();
        const at = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
        return {
          name: control.getAttribute('id') || control.getAttribute('name') || control.tagName,
          labelClass: l.className,
          text: { top: t.top, bottom: t.bottom, left: t.left, right: t.right, width: t.width },
          control: { top: c.top, bottom: c.bottom, left: c.left, right: c.right, width: c.width, height: c.height },
          label: { left: lr.left, right: lr.right, width: lr.width },
          hit: !!at && (at === control || control.contains(at)),
        };
      })
      .filter((f): f is FieldGeometry => f !== null);
  }, selector);
}

// The three properties that together mean "this reads as a labelled field",
// asserted per field rather than on the form as a whole.
export function expectStacked(fields: FieldGeometry[], where: string) {
  // Without this, the whole helper is two loops over an empty array and
  // asserts NOTHING. fieldGeometry() drops any label with no direct text node
  // or no wrapped control, so a refactor to `<label for=x>…</label><input id=x>`
  // would silently empty it — the same silent-false-pass shape ut-docs#301 is
  // about, which is the one failure mode this spec must not have itself.
  expect(fields.length, `${where}: no wrapped-control fields found to check`).toBeGreaterThan(0);
  for (const f of fields) {
    expect(
      f.control.top,
      `${where}: "${f.name}" must start BELOW its own label text, not beside it`,
    ).toBeGreaterThanOrEqual(f.text.bottom - 1);
    expect(
      f.control.width,
      `${where}: "${f.name}" must span its field's full width`,
    ).toBeGreaterThanOrEqual(f.label.width - 2);
    expect(f.hit, `${where}: "${f.name}" must be a real hit-test target`).toBe(true);
    // Height explicitly: every relationship above is satisfiable by a control
    // collapsed to nothing, which is the tender-panel-reachable failure mode.
    expect(
      f.control.height,
      `${where}: "${f.name}" must be a real, tappable height, not collapsed`,
    ).toBeGreaterThan(20);
  }
  for (let i = 1; i < fields.length; i++) {
    expect(
      fields[i].control.top,
      `${where}: "${fields[i].name}" must not share a line with "${fields[i - 1].name}"`,
    ).toBeGreaterThanOrEqual(fields[i - 1].control.bottom);
  }
}

// Fail any spec that triggers a JS error — the class of bug our server-side
// tests can never see (the PIN-in-header regression, dead buttons, …).
//
// Exempts the generic "Failed to load resource: …404…" network message: every
// catalog row renders an unconditional `<img src=".../thumb.png">` with
// `onerror="this.style.visibility='hidden'"` (web/ui/partials/catalog_table.html)
// for items that never had a photo uploaded — a real, pre-existing, already
// gracefully-handled miss, not a JS bug (see universaltill/ut-docs#317, filed
// when this went from latent to visible: the seeded demo items all ship real
// thumb.png files, but any item created afterwards — by hand or by import —
// won't, and the shared e2e till server carries that state across every spec
// in the run, not just the one that created it). Chromium's console API
// doesn't expose which resource 404'd, so this can't be scoped tighter than
// the message text itself; it still fails on every other console error,
// including real image-decode failures, which log a distinct message.
export function watchConsole(page: Page): () => void {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/^Failed to load resource:.*404/.test(m.text())) errors.push(m.text());
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
  ]);
}
