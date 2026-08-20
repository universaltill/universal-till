# Code review — focus-indicator border contrast (WCAG 1.4.11) in curated themes (ut-docs#797)

- **Date:** 2026-08-20
- **Branch:** `feat/797-focus-indicator-contrast`
- **Card:** ut-docs#797 (`complexity:easy`, p3)
- **Reviewer:** independent fresh-context Sonnet subagent (easy-tier routing:
  cheap model builds, fresh-context model reviews)
- **Author (pipeline):** autonomous cloud cycle, on behalf of Farshid Mirza

## What shipped

`input:focus, select:focus, textarea:focus` swapped `border-color` to
`var(--accent)`. WCAG 2.1 SC 1.4.11 (Non-text Contrast) treats a focus
indicator boundary as a UI component and requires ≥3:1 against the adjacent
background. Every curated theme paints the form-control background
`--surface: #ffffff`, and the raw `--accent` measured only:

| theme | `--accent` | contrast vs `#ffffff` |
|---|---|---|
| default | `#2563eb` | 5.17:1 ✅ |
| slate | `#1d4ed8` | 6.70:1 ✅ |
| amber | `#f97316` | 2.80:1 ❌ |
| fresh | `#0ea5e9` | 2.77:1 ❌ |
| monarch | `#10b981` | 2.54:1 ❌ |

**Fix — a dedicated `--focus-border` token** (`web/public/app.css` `:root`,
defaulting to `var(--accent)` so default and slate are unchanged), used by the
three `:focus` boundary rules in place of `--accent`:

- base `input:focus, select:focus, textarea:focus`
- `.split-tender-form input:focus, .split-tender-form select:focus` (a
  class-scoped override that out-specificities the base rule — the same shape
  that shipped ut-docs#305's resting-border miss)
- `.help-hint:hover, .help-hint:focus`

The three failing themes override `--focus-border` with a **darker same-hue
shade** of their own accent (hue and saturation preserved; only ~5–7% darker in
HSL lightness), giving a comfortable margin above the 3:1 floor:

| theme | `--focus-border` | contrast vs `#ffffff` |
|---|---|---|
| amber | `#e76206` | 3.42:1 ✅ |
| fresh | `#0d94d1` | 3.40:1 ✅ |
| monarch | `#0e9e6e` | 3.42:1 ✅ |

`--accent` itself is untouched, so its ~15 non-boundary uses (buttons, tile
prices, tabs, badges, dropzones) — which 1.4.11 does not govern — keep each
theme's identity, mirroring ut-docs#305's `--control-border` approach for the
resting border.

## Tests

New driven spec `e2e/tests/focus-border-contrast-797.spec.ts`: focuses a real
`input` in a real browser and asserts the computed focus `borderTopColor` is
≥3:1 against the control's *measured* background (not a hardcoded white),
across all 5 curated themes × 2 probes (bare + `.split-tender-form`). Same
methodology as the resting-border sibling `form-input-contrast-305.spec.ts`.

- **Driven run:** 10 new + 10 existing (#305) contrast tests — **20 passed** in
  a real Chromium.
- **TDD red check:** reverting the CSS token changes turns exactly the three
  target themes red (amber, fresh, monarch — 8 failures); default and slate
  stay green. Confirms the test fails without the fix.
- `go build ./...` OK; `guard-i18n.sh` clean.

## Independent review findings

**No material issues; no blockers.** The reviewer independently recomputed all
contrast ratios in Python (WCAG sRGB→linear), confirmed hue/saturation
preservation, verified the cascade (default/slate correctly inherit the token
default; monarch inherits white `--surface`), grepped for missed `:focus`
boundary uses of `--accent` (none), and confirmed non-focus `--accent` uses are
untouched.

Non-blocking nits, not acted on:
- `.help-hint` has no dedicated probe (covered transitively via the shared
  token).
- `var(--focus-border, #2b6cb0)`'s literal fallback is now unreachable
  (`--focus-border` is always defined in `:root`) — harmless, and it mirrors the
  pre-existing `var(--accent, #2b6cb0)` pattern it replaced.
- Pre-existing, out of scope: the fixed-blue focus *outline*
  `rgba(37,99,235,.35)` does not retint per theme — not what #797 targets.

## Acceptance criteria

- [x] Focus border for `input`/`select`/`textarea` (and other `:focus` accent
  boundaries) ≥3:1 in all 5 curated themes.
- [x] Verified by computed-colour measurement in a driven run (extends #305's
  methodology; new #797 spec).
- [x] No regression to non-focus uses of `--accent`.
