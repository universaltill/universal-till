# Review: warning text on the plain surface uses --warning-text (ut-docs#5228)

Date: 2026-10-31 · Branch: `fix/5228-warning-text-contrast` · Lane: `lane:cloud-41`
Complexity: easy · Author model: Opus 5.5 · Reviewer: Sonnet 5.5 (fresh context, different model)

## What shipped

- `web/public/app.css`: `.import-system-secrets dd[data-secret-state="not-set"]`
  (the "not set" value on the Import page's system connection) now uses
  `var(--warning-text)` instead of `var(--warning)`. `#d97706` on white is
  3.19:1, below WCAG 1.4.3's 4.5:1. `--warning-text` is 7.09:1 on white in the
  light themes, 5.57:1 in dark (bright amber on `#111827`) and black in
  monochrome. The `--warning-text` token comment now names this use.
- `internal/pages/themes_test.go`:
  - New `TestWarningInkOnlyColoursIcons` scans the served `app.css` with comments
    stripped. Any rule whose `color` is `var(--warning)`, including the fallback
    form `var(--warning, …)`, fails unless its selector is one of the two non-text
    marks: `.tile-badge-lock svg` (an 8px icon) or `.row-warn-icon` (an
    `aria-hidden` ⚠ glyph). Those two are WCAG 1.4.11 marks, where 3:1 is enough.
    The test also asserts that the scan found both of them, so it cannot pass
    vacuously.
  - `TestWarningTextContrastOnWarningTint` now also checks `--warning-text` on
    plain `--bg` and `--surface` (≥ 4.5:1) for every built-in theme.

## Scan for other warning text

`color: var(--warning)` appears in only three rules in `web/public` (vendor
excluded) and `plugins/`. Neither has any inline `style=` or JS use of it.
Rules that use `--warning` as a background set their own dark text. The two
remaining rules are the icon marks above.

## TDD, re-verified

- With the old CSS restored, the new test fails:
  `".import-system-secrets dd[data-secret-state=\"not-set\"]" draws text in var(--warning)`.
- With the fallback form `var(--warning, #d97706)`, it fails the same way, which
  checks review finding 1.
- With the fix, it passes.

## Driven check (Chromium, real app.css + theme files)

Computed `dd` colour against the resolved background:

| Theme | Text | Background | Contrast |
|---|---|---|---|
| default | rgb(146, 64, 14) | white | 7.09:1 |
| dark | rgb(217, 119, 6) | rgb(17, 24, 39) | 5.57:1 |
| monochrome | black | white | 21:1 |

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The scan regex missed `color: var(--warning, #d97706)`, a fallback form the file already uses for backgrounds | Fixed: `var\(--warning\s*[,)]`, mutation-checked |
| 2 | minor | Comments were not stripped, so a comment with an unbalanced brace could swallow the next rule and hide a violation | Fixed: strip with the existing `cssCommentRe` before scanning |
| 3 | nit | A comma selector list would not match the allow-list map | Accepted: fails closed with a clear message |
| 4 | nit | Review record missing | This file |

UX: colour-only change on an existing admin element, with no layout, string or
RTL impact. Manual: no prose or screenshot change is needed, because the text is
unchanged and only its colour darkens.

Verdict: safe to merge.
