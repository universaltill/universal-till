# Code review — form-control border contrast (ut-docs#305)

- **Date:** 2026-08-16
- **Branch:** `pipeline/305-form-input-contrast`
- **Card:** universaltill/ut-docs#305 (`complexity:easy`)
- **Author model:** Sonnet (inline) · **Independent review:** fresh-context
  Sonnet subagent, per the pipeline's easy-tier model routing.

## What shipped

Form-control (`input`/`select`/`textarea`) borders were ~1.2:1 contrast
against their own background using the shared `--border` token — WCAG 2.1
1.4.11 Non-text Contrast wants 3:1 for a UI component's visual boundary.
Only the autofocused field was distinguishable (focus swaps to `--accent`).

- New `--control-border` custom property, aliased to `var(--muted)`
  (already ≥3:1 against `--surface` in the default stylesheet and every
  curated theme) rather than a one-off literal, so it tracks `--muted`
  automatically if a theme ever retunes it.
- The base `input, select, textarea` rule's resting `border-color` swapped
  from `--border` to `--control-border`. `--border` itself is untouched
  everywhere else (~28 decorative divider/card/chip uses) — WCAG 1.4.11
  doesn't cover purely decorative elements, so this stays a small, targeted
  diff rather than re-tuning every border in the app.
- `--control-border: var(--muted)` added to `amber.css`/`fresh.css`/
  `slate.css` (each theme's own `--muted`, already ≥3:1 against its own
  `--surface`); `monarch.css` needs no change — it overrides neither token,
  so it correctly inherits the default's fix.
- New Playwright e2e spec (`e2e/tests/form-input-contrast-305.spec.ts`)
  measures real `getComputedStyle` contrast for all 5 themes
  (default/amber/fresh/monarch/slate) against the actual served
  `/public/app.css` + `/themes/<name>.css`, and asserts the focus border
  still differs from resting.

## Independent review findings (fresh-context Sonnet, all verified & fixed same-branch)

1. **BLOCKER — `.split-tender-form input, .split-tender-form select`
   (app.css) still used `var(--border)`, unfixed.** That selector's
   specificity (class + element) beats the base rule, so it won the
   cascade back to ~1.2–1.3:1 for every input/select inside the
   split-tender payment dialog (`web/ui/pages/index.html`'s `#split-tender-form`)
   — the same class of screen (an operator entering a cash amount) the
   original report specifically called out. The first-draft e2e spec
   couldn't catch it: it only ever probed a bare `<input>` appended to
   `document.body`, exercising the base rule and nothing else. **Fixed:**
   swapped that selector to `--control-border` too, and extended the spec
   to probe both a bare context and an actual `.split-tender-form`-wrapped
   context (matching the app's real markup) across all 5 themes.

2. **Found while re-verifying finding 1 (not by the reviewer, but caught by
   the strengthened test immediately after): a second, pre-existing bug in
   the same selector.** `.split-tender-form input`'s `border` shorthand and
   the generic `input:focus, select:focus, textarea:focus` rule are equal
   specificity (one class + one element vs. one element + one pseudo-class);
   the split-tender rule comes later in the file, so on a tie it silently
   out-ranked the focus rule's `border-color` swap for that one form only.
   Split-tender fields never got a visible focus indicator at all, on
   `main` before this branch touched anything. **Fixed:** added a dedicated
   `.split-tender-form input:focus, .split-tender-form select:focus {
   border-color: var(--accent); }` rule so it reliably wins there too.

Both fixes verified with the actual regression harness: reverted each fix
individually, confirmed the corresponding spec assertion fails with the
real error message (contrast ratio for finding 1; `focus border must
differ from resting border` for finding 2), restored, confirmed green.

## Verified beyond automated tests

- **Contrast math independently recomputed** (WCAG relative luminance) for
  `--border` vs `--surface` (1.19–1.30:1, confirms the bug) and `--muted`
  vs `--surface` (4.76–5.43:1, confirms the fix) in all 5 themes.
- **`--border` confirmed genuinely untouched everywhere else** — grepped
  every remaining `var(--border)` use; all are non-form-control decorative
  uses (dividers, cards, chips, table rules, the on-screen-keyboard key,
  the payment-summary pill). No trace of an earlier draft's accidental
  blanket find-replace across the file.
- **Regression test mechanics verified manually**, not just trusted: for
  each of the two fixes, reverted the CSS, confirmed the exact spec
  assertion failed with the real observed values (e.g. `slate/
  .split-tender-form: border rgb(219, 227, 236) vs background
  rgb(255, 255, 255) must be >= 3:1`, ratio 1.2954…), restored, confirmed
  pass.
- **Full e2e suite** (128 Playwright specs, default project): 127 passed,
  1 pre-existing failure (`catalog-image-to-till.spec.ts`) confirmed
  identical with the branch's changes stashed out — unrelated to this
  branch (an image-loading/asset timing issue in this sandbox, not a CSS
  regression).
- `go build ./...` and `scripts/ci/guard-i18n.sh` clean (no Go/i18n surface
  touched, but no such change was introduced either).

## Verdict

Safe to merge. Both the review's blocker and the pre-existing focus-gap it
led to are fixed and regression-tested; nothing else outstanding on this
card.

## Deferred (filed as a new Backlog card, not fixed here)

The independent review also found `--accent` vs `--surface` — the FOCUS
indicator's own contrast, unrelated to this ticket's resting-border scope —
is under 3:1 in 3 of 5 themes (amber 2.80:1, fresh 2.77:1, monarch 2.54:1;
default 5.17:1 and slate 6.70:1 are fine). This predates this branch (the
focus rule wasn't touched) and is a distinct WCAG 1.4.11 gap on its own
axis (focus-indicator contrast, not resting-border contrast) — filed
separately rather than scope-creeping into this diff.
