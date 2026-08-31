import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#797: the FOCUS-state border of a form control
// (`input:focus, select:focus, textarea:focus` swaps border-color to the
// theme accent) is itself a UI-component boundary WCAG 2.1 1.4.11 Non-text
// Contrast covers -- it wants 3:1 against the adjacent background. The raw
// --accent cleared that in default (5.17:1) and slate (6.70:1) but not in
// amber (2.80:1), fresh (2.77:1) or monarch (2.54:1).
//
// Fix: a dedicated --focus-border token (defaulting to --accent in app.css,
// so default/slate are unchanged) that amber/fresh/monarch override with a
// darker same-hue shade. --accent itself is left alone so its ~15 other,
// non-boundary uses (buttons, tile prices, tabs) -- which 1.4.11 doesn't
// govern -- keep each theme's identity.
//
// This is the focus-state sibling of the resting-border spec
// (form-input-contrast-305.spec.ts) and measures the same way: computed
// colours read off a REAL focused element in a real browser
// (getComputedStyle after .focus()), against the real served theme CSS that
// web/ui/layouts/base.html links -- not the stylesheet source, not a
// screenshot. The probe list mirrors 305's so a future class-scoped
// `:focus` border override that out-specificities the base rule is caught
// here instead of silently winning the cascade.

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

const PROBES: Array<{ label: string; wrapperHTML: string | null }> = [
  { label: 'bare (base input/select/textarea:focus rule)', wrapperHTML: null },
  // app.css's `.split-tender-form input:focus, .split-tender-form select:focus`
  // redeclares its own focus border-color -- the same class-scoped override
  // that shipped 305's resting-border miss, so it's covered here too.
  { label: '.split-tender-form (split-tender payment dialog)', wrapperHTML: '<div class="split-tender-form"></div>' },
];

test.describe('form-control focus borders meet WCAG 1.4.11 non-text contrast (ut-docs#797)', () => {
  for (const theme of THEMES) {
    for (const probe of PROBES) {
      test(`${THEME_LABELS[theme]} theme: ${probe.label} focus border is >= 3:1 against its own background`, async ({
        page,
      }) => {
        const assertClean = watchConsole(page);
        await page.goto('/');
        await page.waitForSelector('.pos-container');
        if (theme !== 'default') {
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
          const background = getComputedStyle(input).backgroundColor;
          input.focus();
          const focusBorder = getComputedStyle(input).borderTopColor;
          (wrapper ?? input).remove();

          const l1 = luminance(parseRGB(focusBorder));
          const l2 = luminance(parseRGB(background));
          const lighter = Math.max(l1, l2);
          const darker = Math.min(l1, l2);
          const ratio = (lighter + 0.05) / (darker + 0.05);

          return { focusBorder, background, ratio };
        }, probe.wrapperHTML);

        expect(
          result.ratio,
          `${THEME_LABELS[theme]}/${probe.label}: focus border ${result.focusBorder} vs background ${result.background} must be >= 3:1`,
        ).toBeGreaterThanOrEqual(3);

        assertClean();
      });
    }
  }
});
