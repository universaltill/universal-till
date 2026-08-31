import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, fieldGeometry, expectStacked } from './helpers';

// ut-docs#300: the deposit-refund (Pfandrückgabe) payout dialog shipped with
// its two fields running inline into one broken row -- "Manager PIN" sat to
// the RIGHT of the amount input and the PIN's own input wrapped onto the line
// below, so it was not obvious which input belonged to which label, on a form
// that pays cash out of the drawer.
//
// The root cause was a stylesheet default, not this dialog's markup: app.css
// styled `label` with no `display` at all, so a <label> wrapping its own
// control stayed inline. Every other form in the file overrode that
// explicitly (.catalog-form, .split-tender-form, .translations-controls,
// .setup-card) and this dialog was simply never given such a rule -- i.e.
// stacking was accidental, opt-in, and any NEW form would inherit the bug.
// The fix stacks any label that wraps a text-ish control globally, so a new
// form cannot reintroduce it by forgetting a class.
//
// Asserted GEOMETRICALLY rather than by text/function on purpose: ut-docs#301
// records that this dialog was driven in a real browser the day before and
// passed, because the check only asserted the strings were German. Nobody
// looked at the layout. Geometry is what that check was missing.
//
// The tender-panel-reachable.spec.ts lesson applies as well -- a bounding box
// can be perfectly plausible for an element inside a collapsed ancestor -- so
// every control here is also hit-tested with elementFromPoint, not merely
// measured.

async function openPfand(page: Page, url = '/') {
  await page.goto(url);
  await page.waitForSelector('.pos-container');
  await page.locator('[data-testid="kiosk-pfand-open"]').click();
  await expect(page.locator('#pfand-modal')).toBeVisible();
}

test.describe('wrapped-control labels stack above their inputs (ut-docs#300)', () => {
  test('deposit-refund dialog: both fields stack, at 1280x800', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await openPfand(page);

    const fields = await fieldGeometry(page, '#pfand-modal');
    expect(fields.map((f) => f.name)).toEqual(['pfand-amount', 'manager_pin']);
    expectStacked(fields, 'pfand @1280x800');
    assertClean();
  });

  test('deposit-refund dialog: still stacked at the 10-inch kiosk size 1024x600', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await openPfand(page);

    expectStacked(await fieldGeometry(page, '#pfand-modal'), 'pfand @1024x600');
    assertClean();
  });

  // ut-docs#292's German terms are materially longer than the English and are
  // exactly what a naive one-line layout re-breaks on.
  //
  // German is NOT driven via ?lang=de here, deliberately: it ships as a
  // language-pack PLUGIN (ut-plugin-language-de/locales/de.json, ut-docs#292),
  // not a core locale — the shipped core locales are en/ar/fa/tr. A till
  // without that plugin resolves de → en and renders English, so `?lang=de`
  // against this suite's till would assert nothing at all while looking like
  // a German check. (The first draft of this spec did exactly that and passed
  // on the English text — the ut-docs#301 failure mode, caught by looking at
  // the screenshot.) So the real German strings are substituted into the live
  // DOM instead, which is what the acceptance criterion is actually about:
  // the layout tolerating the longest translation.
  test('deposit-refund dialog: the longer German terms do not re-break the layout', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await openPfand(page);

    const applied = await page.evaluate(() => {
      const de: Record<string, string> = {
        'pfand-modal-title': 'Pfandrückgabe',
        'pfand-amount': 'Betrag (£) ',
        manager_pin: 'Manager-PIN ',
      };
      document.getElementById('pfand-modal-title')!.textContent = de['pfand-modal-title'];
      const out: string[] = [];
      document.querySelectorAll('#pfand-modal label').forEach((l) => {
        const control = l.querySelector('input') as HTMLInputElement;
        const key = control.id || control.name;
        const text = Array.from(l.childNodes).find(
          (n) => n.nodeType === Node.TEXT_NODE && (n.textContent || '').trim() !== '',
        );
        if (text && de[key]) {
          text.textContent = de[key];
          out.push(de[key].trim());
        }
      });
      return out;
    });
    // Guard the substitution itself, so this can never quietly become a
    // second English run again.
    expect(applied).toEqual(['Betrag (£)', 'Manager-PIN']);
    await expect(page.locator('#pfand-modal')).toContainText('Pfandrückgabe');

    const fields = await fieldGeometry(page, '#pfand-modal');
    expectStacked(fields, 'pfand @de-terms');
    // Nothing may overflow the dialog's own content box either.
    const dialog = await page.locator('#pfand-modal').boundingBox();
    for (const f of fields) {
      expect(f.control.right, `de: "${f.name}" overflows the dialog`).toBeLessThanOrEqual(
        dialog!.x + dialog!.width + 1,
      );
    }
    assertClean();
  });

  // RTL is where a literal left/right shows up instantly: the label text and
  // its control must share an inline-START edge, which is the RIGHT edge here.
  test('deposit-refund dialog: stacks and aligns to the inline start in RTL (fa)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await openPfand(page, '/?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

    const fields = await fieldGeometry(page, '#pfand-modal');
    expectStacked(fields, 'pfand @fa/RTL');
    for (const f of fields) {
      expect(
        Math.abs(f.control.right - f.text.right),
        `fa: "${f.name}" must align to the inline start (right), i.e. logical properties, not left/right`,
      ).toBeLessThanOrEqual(2);
    }
    assertClean();
  });

  // NOTE: the change-Manager-PIN form (/pin) is the SECOND instance of this
  // same defect -- three password fields in a plain .card that no scoped rule
  // covered either, and the reason this fix is a global default rather than a
  // #pfand-modal rule. It is NOT asserted here: GET /pin requires a real
  // authenticated operator, and this project's till runs auth off, so /pin
  // only ever redirects to /login. It is asserted in login.spec.ts instead,
  // which is the one spec with a genuine logged-in session.

  // The other half of the contract: rows that are deliberately HORIZONTAL must
  // stay horizontal. `.set-row` is `display:flex; align-items:center` with no
  // flex-direction -- structurally the same trap ut-docs#300 warned about for
  // `.modifier-option`, and specificity does not save it, because the cascade
  // is per-property: a container that never declares `flex-direction` cannot
  // out-rank a rule that does, whatever its selector weight. The independent
  // review caught this stacking (and centring, via align-items) three
  // manager-facing Settings rows, which no existing test covered --
  // pages.spec.ts only asserts /settings contains the text "System Settings".
  test('deliberately horizontal Settings rows stay horizontal', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    await page.waitForSelector('.set-row');

    // Select by the LABEL's class, not the control's name: /settings has a
    // fourth `select name="mode"` (the printer mode, settings.html:390) which
    // is a plain label in a .field-pair and SHOULD stack. Filtering by name
    // caught that one too and failed for the right-looking wrong reason.
    const rows = await fieldGeometry(page, 'body');
    const inline = rows.filter((r) => r.labelClass.split(/\s+/).includes('set-row'));
    expect(inline.length, 'expected the osk / display-mode / payment-default set-rows').toBeGreaterThanOrEqual(3);
    for (const r of inline) {
      expect(
        r.control.top,
        `settings: "${r.name}" must stay on ONE line with its label, not stack`,
      ).toBeLessThan(r.text.bottom);
      // align-items:center would also centre it in the card; it must not.
      expect(
        r.control.width,
        `settings: "${r.name}" must stay its natural width, not stretch/centre`,
      ).toBeLessThan(r.label.width - 2);
    }
    assertClean();
  });

  // ADR-0020 guard. The item-customization modal's options are
  // `.modifier-option { display: flex; align-items: center }` with NO
  // flex-direction, so a careless stacking rule would flip every modifier
  // option into a column and break item customization -- ut-docs#300 called
  // this out explicitly. Probing computed style against live DOM tests the
  // stylesheet itself, which is the thing that can regress; the demo catalog
  // carries no modifier groups to open the real picker with.
  test('stacking never reaches checkbox/radio rows (ADR-0020 modifier options)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const directions = await page.evaluate(() => {
      const probe = document.createElement('div');
      probe.innerHTML = `
        <label class="modifier-option" data-p="modifier-option"><input type="checkbox"><span>Extra shot</span></label>
        <label data-p="bare-checkbox"><input type="checkbox"> Sold by weight</label>
        <label data-p="bare-radio"><input type="radio"> Receive</label>
        <label data-p="bare-text">Amount <input type="text"></label>
        <label data-p="bare-password">Manager PIN <input type="password"></label>
        <label data-p="bare-select">Type <select><option>a</option></select></label>`;
      document.body.appendChild(probe);
      const out: Record<string, string> = {};
      probe.querySelectorAll('label').forEach((l) => {
        const s = getComputedStyle(l);
        out[l.dataset.p as string] = `${s.display}/${s.flexDirection}`;
      });
      probe.remove();
      return out;
    });

    // A checkbox/radio row keeps the control beside its text...
    expect(directions['modifier-option'], 'ADR-0020 modifier options must stay a row').toBe('flex/row');
    expect(directions['bare-checkbox'], 'checkbox toggle rows must not stack').not.toContain('column');
    expect(directions['bare-radio'], 'radio rows must not stack').not.toContain('column');
    // ...while a text-ish control stacks.
    expect(directions['bare-text']).toBe('flex/column');
    expect(directions['bare-password'], 'the /pin form shape must stack').toBe('flex/column');
    expect(directions['bare-select']).toBe('flex/column');
    assertClean();
  });
});
