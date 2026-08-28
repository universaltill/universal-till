# Code review — deposit-refund dialog no longer auto-focuses a field (ut-docs#1248)

- **Date:** 2026-08-28
- **Branch:** `fix/1248-deposit-refund-double-osk`
- **Reviewer:** independent reviewer (fresh-context, different session — the
  build/review split this pipeline uses for `complexity:easy` cards)
- **Verdict: SAFE TO MERGE AS-IS.** No blocking issues. Two non-blocking
  follow-ups from the review were folded in before merge (see below); one
  more was filed as a separate ticket (ut-docs#1262).

## What shipped

Reported live: "when I click on deposit refund, 2 keyboards, opening."
`<dialog id="pfand-modal">.show()` was silently moving DOM focus to
`#pfand-amount` — verified against a real Chromium build, not assumed —
even though the field carries no `autofocus` attribute. That's exactly the
"programmatic focus" `osk.js`'s own design says must never pop a keyboard
(ut-docs#155); the codebase's one other `.show()`'d dialog with an
auto-focused field (`#hold-modal`) opts into it deliberately via a real
`autofocus` attribute and accounts for it (ut-docs#1048). This one never
did — it was an engine default nobody designed for.

On the happy path this was harmless (osk.js's up-front DOM sweep,
ut-docs#1022, already suppresses the native keyboard on every OSK-able
field at page load). The exposure is a device whose touch is misreported as
mouse input, so `osk.js`'s `auto`-mode enable never fires and the sweep
never runs — confirmed real and current on this exact hardware class the
same day (ut-docs#1238, Android Chrome's desktop-site mode on a large
tablet). There, the accidental focus pops the real native keyboard
unsuppressed; a subsequent deliberate tap then opens this app's own OSK on
top of it.

**Fix:** the button's `onclick` now saves the pre-click `activeElement`,
calls `.show()`, and — only if focus landed *inside* the dialog (i.e. the
accidental case) — blurs it and restores focus to whatever was focused
before. A plain unconditional `blur()` was the first draft; the review
flagged it would also strip a keyboard user's own focus from the button
itself (no focus trap on this non-modal dialog), so this was tightened to
save/restore instead, verified empirically for both mouse and keyboard
activation (see Verification).

Two tests in `e2e/tests/deposit-refund-osk-1248.spec.ts` (Playwright, real
Chromium): no field is auto-focused on open; the custom OSK stays a
singleton across both fields (a companion assertion, not itself a
regression guard for this specific bug — see comment in the test file,
added after review found the original comment overstated what it checks).

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` / `gofmt -l .` | pass / empty |
| `bash scripts/ci/guard-i18n.sh` | pass — no new user-facing strings |
| `bash scripts/ci/guard-osk-loaded.sh` | pass |
| e2e: `deposit-refund-osk-1248`, `form-label-layout-300`, `osk-central-guard`, `autofill-suppression-400`, `phone-width-layout-413` | 32/32 pass |

### Independent re-verification of the root-cause claim

The reviewer did not take the auto-focus claim on trust: wrote their own
throwaway probe against the unfixed code and confirmed
`document.activeElement` really was `#pfand-amount` immediately after
`.show()`, with `hasAutofocus: false` and `inputmode: null` (unsuppressed).
Ran the new spec against `HEAD~1` (pre-fix) and confirmed the first test
fails for the reported reason, passes once the fix is in.

### Post-review tightening, verified

After the review flagged the plain `blur()` as a potential a11y regression
(button's own focus stripped even on a legitimate keyboard activation, and
this dialog has no focus trap since it uses `.show()` not `.showModal()`),
re-tested both activation paths against the tightened save/restore fix:

```
KEYBOARD ACTIVATION (.focus() + Enter) -> activeElement: kiosk-pfand-open
MOUSE/TAP ACTIVATION (.click())        -> activeElement: kiosk-pfand-open
```

Button correctly keeps its own focus in both cases now — the accidental
in-dialog focus is still removed, but nothing legitimate is lost.

## Findings (from review) and disposition

1. **Non-blocking — misleading test comment.** The second test's comment
   claimed to guard "the regression this ticket is about," but osk.js's
   custom keyboard only ever opens from a `click` (never `focus`), so it
   was never reachable via the accidental-focus path this fix removes —
   the test passed identically before and after. **Fixed**: reworded to
   describe what it actually guards (the OSK singleton assumption), not
   this specific bug.
2. **Non-blocking — a11y: unconditional `blur()` drops legitimate keyboard
   focus, no focus trap on this non-modal dialog.** **Fixed**: tightened to
   save/restore, verified above.
3. **Non-blocking — root cause is broader than this one dialog.** `osk.js`'s
   `auto`-mode enable-detection (`touchstart`-only) leaves every field
   unsuppressed on a misdetected-touch device until some real touch
   happens, not just this one dialog's field. **Filed separately**:
   ut-docs#1262 — widening the lazy-enable trigger is a real (if smaller-
   blast-radius) architectural change to `osk.js` itself, out of scope for
   a `complexity:easy` single-dialog fix.

## Checked and found clean

- No raw SQL, no new user-facing strings (pure inline JS), no i18n/RTL
  surface touched.
- No other spec touching `#pfand-modal` (`phone-width-layout-413`,
  `autofill-suppression-400`, `form-label-layout-300`) asserts on focus
  state — nothing relies on the old accidental-focus behavior.
- Offline-first: untouched: no network/checkout path involved.
