import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#305: form-control borders were ~1.2:1 against their own
// background (--border, slate-200, on a white --surface) -- WCAG 2.1
// 1.4.11 Non-text Contrast wants 3:1 for a UI component's visual boundary.
// Only the autofocused field was distinguishable, via its focus border.
//
// Fix: a dedicated --control-border token (aliased to the already-legible
// --muted in app.css and every curated theme) replaces --border specifically
// on `input, select, textarea`'s resting border-color. --border itself is
// untouched -- it still styles purely decorative dividers/cards, which
// 1.4.11 doesn't cover, so this stays a small, targeted diff rather than
// re-tuning every border in the app.
//
// Measured the same way the original report was: computed colours read off
// a REAL element in a real browser (getComputedStyle), not the stylesheet
// source and not a screenshot. Each curated theme's own stylesheet is
// layered on top of app.css via the exact /themes/<name>.css URL
// web/ui/layouts/base.html itself links, so this exercises the actual
// served CSS, not a copy of it.
//
// Independent review (ut-docs#305) caught a real miss the first draft of
// this test could not have caught: a bare probe <input> only ever exercises
// the generic base rule, but .split-tender-form's own higher-specificity
// selector redeclared its own `border: 1px solid var(--border)` and won the
// cascade, so the split-tender payment dialog still shipped the original
// ~1.2:1 defect while this spec reported green. PROBES below now cover
// every wrapper class this stylesheet is known to give its own `border:`
// declaration on an input/select -- adding a probe here is how a future
// class-scoped override gets caught instead of silently winning the
// cascade again.

const THEMES = ['default', 'amber', 'fresh', 'monarch', 'slate'] as const;

// ut-docs#864: 'default' here is a control-flow key ("skip theme stylesheet
// injection"), not a real selectable theme -- there's no web/public/themes/
// default.css. The till this spec drives boots with UT_THEME unset, which
// internal/config/config.go defaults to "monarch" (e2e/run-till.sh never
// overrides it), so what actually renders for the 'default' key is
// monarch's live CSS. This label map makes the test title say that, rather
// than implying a genuine themeless/base-app.css state that was never
// actually measured.
const THEME_LABELS: Record<(typeof THEMES)[number], string> = {
  default: 'server-default (monarch)',
  amber: 'amber',
  fresh: 'fresh',
  monarch: 'monarch',
  slate: 'slate',
};

// [label, HTML to insert the probe <input>/<select> into]. `{{control}}`
// is replaced with the actual control element by the in-page script below.
const PROBES: Array<{ label: string; wrapperHTML: string | null }> = [
  { label: 'bare (base input/select/textarea rule)', wrapperHTML: null },
  // web/ui/pages/index.html's split-tender dialog -- the one real miss an
  // earlier draft of this fix shipped (app.css's .split-tender-form input,
  // .split-tender-form select selector out-specificities the base rule).
  { label: '.split-tender-form (split-tender payment dialog)', wrapperHTML: '<div class="split-tender-form"></div>' },
];

test.describe('form-control borders meet WCAG 1.4.11 non-text contrast (ut-docs#305)', () => {
  for (const theme of THEMES) {
    for (const probe of PROBES) {
      test(`${THEME_LABELS[theme]} theme: ${probe.label} resting border is >= 3:1 against its own background`, async ({
        page,
      }) => {
        const assertClean = watchConsole(page);
        await page.goto('/');
        await page.waitForSelector('.pos-container');
        if (theme !== 'default') {
          // The exact stylesheet base.html links for a non-default theme
          // (real served CSS, not a copy pasted into the test).
          await page.addStyleTag({ url: `/themes/${theme}.css` });
        }

        const result = await page.evaluate((wrapperHTML) => {
          function srgbToLinear(c: number) {
            const s = c / 255;
            return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
          }
          function luminance(rgb: [number, number, number]) {
            const [r, g, b] = rgb.map(srgbToLinear);
            return 0.2126 * r + 0.7152 * g + 0.0722 * b;
          }
          function parseRGB(css: string): [number, number, number] {
            const m = css.match(/rgba?\(([^)]+)\)/);
            if (!m) throw new Error(`unparseable colour: ${css}`);
            const parts = m[1].split(',').map((n) => parseFloat(n.trim()));
            return [parts[0], parts[1], parts[2]];
          }

          const input = document.createElement('input');
          input.type = 'text';
          let host: HTMLElement = document.body;
          let wrapper: HTMLElement | null = null;
          if (wrapperHTML) {
            wrapper = document.createElement('div');
            wrapper.innerHTML = wrapperHTML;
            host = wrapper.firstElementChild as HTMLElement;
            document.body.appendChild(wrapper);
          }
          host.appendChild(input);
          const resting = getComputedStyle(input);
          const restingBorder = resting.borderTopColor;
          const background = resting.backgroundColor;
          input.focus();
          const focusBorder = getComputedStyle(input).borderTopColor;
          (wrapper ?? input).remove();

          const l1 = luminance(parseRGB(restingBorder));
          const l2 = luminance(parseRGB(background));
          const lighter = Math.max(l1, l2);
          const darker = Math.min(l1, l2);
          const ratio = (lighter + 0.05) / (darker + 0.05);

          return { restingBorder, focusBorder, background, ratio };
        }, probe.wrapperHTML);

        expect(
          result.ratio,
          `${THEME_LABELS[theme]}/${probe.label}: border ${result.restingBorder} vs background ${result.background} must be >= 3:1`,
        ).toBeGreaterThanOrEqual(3);
        // The focus state must still read as visually distinct from resting
        // (don't fix resting contrast by making focus less obvious).
        expect(
          result.focusBorder,
          `${THEME_LABELS[theme]}/${probe.label}: focus border must differ from resting border`,
        ).not.toBe(result.restingBorder);

        assertClean();
      });
    }
  }
});
