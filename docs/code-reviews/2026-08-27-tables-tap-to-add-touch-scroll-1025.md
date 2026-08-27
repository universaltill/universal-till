# Code review: floor-plan tap-to-add + touch-scroll scoping (ut-docs#1025)

**Date:** 2026-08-27
**Author (pipeline lane):** `lane:cloud-54`, Fable dev subagent + Sonnet
orchestrator, independent Sonnet Tester subagent
**Card:** universaltill/ut-docs#1025 (scope split this cycle — non-table
fixtures moved to ut-docs#1150, a separate, larger follow-up)

## What shipped

Two independent, bounded fixes to the table floor-plan editor
(`web/ui/pages/tables.html`, `internal/pages/tables_page.go`):

1. **Tap-to-add a table in place.** `POST /api/tables` now accepts optional
   `pos_x`/`pos_y` form fields (falls back to canvas centre when absent —
   the existing bottom-of-page form's behavior is unchanged). A new
   `#table-add-modal` dialog, opened by a `click` on the floor-plan
   background while in edit mode, pre-fills the tapped (clamped) position
   into hidden inputs and submits as a plain full-page POST — the same
   mechanism the pre-existing add-table form already uses.
2. **Touch-scroll no longer swallowed outside a drag.** `touch-action: none`
   on the floor-plan SVG was unconditional; scoped it to
   `#floorplan-section.editing` so live-view panning works normally. This is
   a narrow, hardware-independent contribution to ut-docs#1021 (the general
   cross-page touch-scroll regression, which stays `blocked:env` pending
   real Pi hardware) — not a fix for #1021 itself, cross-referenced in both
   issues.

## Independent review findings

An independent Sonnet Tester subagent drove the real app (built binary,
screenshotted light/dark\* theme × en/ar/fa locales, deliberately broke and
restored both the Go test and the e2e touch-action assertion to confirm
neither is a tautology) and found two real defects in the initial
implementation, both since fixed in this same commit:

1. **OSK unreachable inside the dialog (confirmed, not speculative).** The
   dialog opened via `dialog.showModal()`, which makes everything outside
   the dialog **inert** — including this product's custom on-screen
   keyboard (`#osk`, appended to `document.body`, not inside the dialog).
   Verified with `hasTouch: true` + `locator.tap()` on an OSK key: it timed
   out, the field's value never changed. This directly contradicts the
   product's own established precedent — `#hold-modal`, `#pfand-modal`, and
   the manager-elevation dialog all deliberately use non-modal `.show()`
   for exactly this reason (see `app.css`'s `#hold-modal` comment,
   ut-docs#46). **Fix:** switched to `.show()` and added the same explicit
   `position: fixed; inset-block-start: 8vh; …` CSS override already
   applied to those three dialogs.
2. **Dialog paint diverged from its own measured geometry under
   `dir="rtl"`.** At 800×600 in `fa`, the dialog visually rendered clipped
   at the viewport edge while `getBoundingClientRect()` reported it fully
   on-screen — isolated to `dir="rtl"` + the native `<dialog>` top-layer
   centering path, not to Persian content. The same fix (removing reliance
   on native top-layer centering in favor of the explicit `position: fixed`
   override) resolves this as a side effect, per the Tester's own
   hypothesis.

## Re-verification after the fix (this review pass, not the original Tester run)

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./internal/pages/...`
  clean.
- `go test ./internal/pages/... ./internal/data/...` — pass (unaffected by
  the CSS/JS-only fix).
- `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-data-access.sh` —
  all pass.
- `npx playwright test tests/tables-tap-to-add-1025.spec.ts
  tests/tables-keyboard-reposition-826.spec.ts` — **7/7 pass** against a
  real server, confirming `.show()` doesn't break the dialog's `[open]`
  attribute, `.close()`, or autofocus behavior the tests assert on.
- One-off verification script (real browser, `fa` locale, 800×600, edit
  mode → tap → dialog open): `document.body.inert === false` (OSK no longer
  blocked) and `dialog.getBoundingClientRect()` fully within the 0..800
  viewport (`left:162, right:638, top:48` — matches the `8vh`/
  `margin-inline:auto` CSS exactly). Both defects confirmed fixed.

\* Dark theme: this repo has no dark theme (four shipped curated themes are
all light-toned) — Tester confirmed the dialog's only styling
(`.modifier-modal`, theme tokens, no hardcoded colors) is identical to the
pre-existing `#hold-modal`'s, so there's nothing theme-specific to this
change to verify further.

## Translation quality (ar/fa/tr help-doc updates)

The homelab Ollama translator was unreachable from both the Dev and Tester
sandboxes; both independently read the ar/fa/tr sentences directly (native
word order, correct grammar/agglutination, no English-order tell) and found
them competent, idiomatic translations — not machine-literal.

## Scope discipline

Non-table fixtures (walls/doors/decorative objects, new shape vocabulary,
schema change) were split out to ut-docs#1150 at the BA step — genuinely
separable, schema-touching, larger work that would have made this diff
unreviewable if bundled in.

## Not verified here (accepted, tracked elsewhere)

- Real touch hardware scroll behavior — ut-docs#1021, needs a real Pi.
- `-race` on `internal/pages` — this package is flagged by the repo's own
  Makefile as needing an extended timeout under `-race`
  (`make test-race-pages`); not run in this review pass given the change is
  CSS/JS + a single new form-field parse, not concurrency-sensitive code.
- Turkish rendering (visual) — only the translated text was read, not
  screenshotted; lower risk since the fix is layout-identical across
  locales and the ar/fa checks already exercise the RTL path this change
  actually touches.

## Merge

Feature branch `fix/1025-tables-tap-to-add-touch-scroll`, PR references
`Closes universaltill/ut-docs#1025`, `merge_method: "merge"` per
ut-docs#250 (never squash/rebase — GitHub's merge API re-attributes
squashed/rebased commit content to the merging account's real personal
email regardless of the commit's own `git config` author).
