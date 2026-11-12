# Review: phone discard question no longer covers the record dialog's Lock (ut-docs#6131)

Date: 2026-11-12 · Branch: `fix/6131-phone-confirm-clears-dialog-lock` · Author model: Sonnet · Reviewer: Opus 5.5 (a different model from the author), independent of Dev and Tester

## What shipped

- `web/public/app.css`, inside the existing `@media (max-width: 480px)` block:
  while a full-screen `.record-dialog` (or #6125's `.dialog-beside-chrome`,
  not on main yet, so that selector does nothing for now) is `[open]`,
  `#ut-confirm` is anchored to the bottom, above the status row, instead of
  just under the top bar (#6118). Its height is capped so its top stays
  below the dialog head (`11rem`, while the measured head is 160.7px). The
  dialog head (title, Save/Close, conn chip, exit-to-OS, Lock) stays the hit
  target. A `body.osk-padded` variant ends the question above the reserved
  keyboard band. The #6118 comment is updated.
- New `e2e/tests/phone-record-dialog-confirm-lock-6131.spec.ts` (7 tests,
  added to `AUTH_ONLY_SPECS` because Lock only renders with a real session).
- `web/help/img/manifest.json`: the surface hash is refreshed with
  `update-docs-shots-surface-hash.sh`. No docs shot shows an open till-confirm
  over a record dialog, so no rendered pixel in the manual changes. The
  commit needs the `Docs-Shots-Unchanged: true` trailer.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | The `body.osk-padded` rule moved the bottom edge up by the keyboard band (~17rem) but kept the non-OSK `max-block-size`. A long question (e.g. a long translation) then grew back up over the head. Probe at 320×568: question top **−31.8px**, `elementFromPoint` on Lock = `ut-confirm-msg`. The spec's osk test used only the short question, so it missed this. | **Fixed.** The osk-padded rule re-caps `max-block-size` to `100vh/100dvh − --osk-reserved-height − 11.5rem`. New test "osk-padded + long question" added. It fails without the cap (top −54.5 vs head bottom 160.7) and passes with it. |
| 2 | info | The bottom offset reserves `--phone-sb-h`. A full-screen record dialog (z 500) already covers the page status bar (z 460), so that gap shows dialog content, not the status bar. | Accepted. The gap is harmless and keeps the box clear of the bottom edge and the home indicator. |
| 3 | info | Head height checked at 320×568 on /categories, /locations, /promotions and /kitchen-stations: 160.7px everywhere. The h3 is `nowrap` with an ellipsis, so a long title does not grow the head, and the 11rem cap holds. | No action. |

CSS checks with no finding:
- **`:has()` scope.** The rule needs an *open* `.record-dialog` / `.dialog-beside-chrome` anywhere in the document, inside `max-width: 480px`. Specificity is (1,2,1), above the plain `#ut-confirm` phone rule (1,0,0). The osk line is (1,3,2) and overrides that. No other rule targets `#ut-confirm`. A browser without `:has()` drops the rule and keeps the #6118 behaviour, which is a safe fallback. `:has()` is already used in app.css.
- **z-index and scrim.** These are unchanged: question z 590 over the dialog's z-500 stacking context, below `#osk` (1000). No new `showModal()`; till-confirm.js is untouched.
- **dvh fallback.** Each `max-block-size` comes as a `vh` declaration followed by a `dvh` one, as in the existing rules.
- **Logical properties.** Only `inset-block`, `inset-block-end` and `max-block-size` are used, and `inset-inline`/`margin-inline: auto` are inherited unchanged. The RTL test confirms the box is centred to within 1px. `guard-rtl-logical-css.sh` is green.
- **1024×600 kiosk floor.** All new rules are inside `max-width: 480px`. The spec's 1024 test pins top = 48px (8vh), and the Tester ran `native-confirm-6116` (AC12, real Lock click at 1024) green.
- **Self-order kiosk pages.** No `self_order*.html` template renders a `.record-dialog`, so the selector can never match there.
- **Other dialogs that open `#ut-confirm`.** The change applies to any question raised while a record dialog is open, for example a destructive Delete in the head (`hx-confirm`) as well as Close/discard. The bottom placement is correct for all of them, because in each case the dialog head is what must stay reachable. Without an open record dialog, #6118's top-bar placement is unchanged.

## Does the spec test the AC?

AC1 (head clear, Lock is the `elementFromPoint` hit), AC2 (Cancel keeps "Scratch"), AC3 (real, non-forced Lock click → `/login`), AC4 (dark via `#theme-css` swap; `dir=rtl` + centring), AC5 (bottom ≤ innerHeight − `--phone-sb-h`; osk-padded bottom ≤ innerHeight − body `padding-block-end`), AC6 (1024 top = 48px). AC7 is covered by the guards. Long-text scrolling is tested both with and without the OSK band (the second case after finding 1). One limit: the osk-padded state is synthetic (the class is added by script, no keyboard is opened). In real use, focusing Cancel normally hides the OSK, so this state is transient.

## Evidence

- Spec after the fix: `npx playwright test --project=auth phone-record-dialog-confirm-lock-6131` → **7 passed** (re-run by the reviewer).
- Reviewer false-pass check for finding 1: removed the new osk cap → 1 failed (the new test), 6 passed. Restored the file (`cmp` identical) → 7 passed.
- Tester false-pass check (from the cycle notes): with the app.css hunk reverted → 5 failed / 1 passed (the 1024 no-leak test passes both ways, as expected). The failure is the real defect: question top 64.5 vs head bottom 160.7, hit = `ut-confirm`. With only the osk rule removed, only the osk test fails.
- Tester gate: `gofmt` clean; `go build`/`go vet` OK; `golangci-lint` 0 issues; `shellcheck` 0 issues; `go test ./...` 108 ok + `internal/plugins` OK under CI's `-timeout 20m` (744s; it hit only the local 10m default); 154/154 `build`-job guard invocations pass. Regression e2e (`native-confirm-6116`, `phone-lock-over-scrim-6118`, `categories-record-dialog-2010`, `categories-record-dialog-main-till-lock-3605`, 6131): 46 passed, 0 failed.
- Reviewer re-ran after the fix: `guard-rtl-logical-css`, `guard-no-showmodal`, `guard-i18n`, `guard-fixed-dates` and `run-docs-shots-guard` (after the surface-hash refresh) are all green. No Go changed.
- Screenshots seen. The reviewer looked at `6131-360x640-light.png` (question low at y≈443–575, the head with Save/Close/Online/exit/Lock fully clear) and `6131-360x640-osk-padded.png` (question between the head and the reserved band). The Tester also looked at the dark, fa-RTL and 1024×600 shots: the dark box is legible, the RTL box is centred, and the 1024 layout is unchanged.

## Not verified

- **WebKit / iOS Safari.** `npx playwright install webkit` is blocked by the sandbox proxy (403). The spec is in `AUTH_ONLY_SPECS` (part of `EXCLUSIVE_SPECS`), and the `webkit-phone` lane `testIgnore`s that list, so CI's WebKit job does not run it either. This is the same as the existing 6118 / phone-drawer-* auth specs.
- **No real phone.** Touch is emulated (Playwright `hasTouch`/`isMobile`, Chromium).
- **No real OSK** on a phone-width record dialog (`body.osk-padded` is set synthetically), and no long German translation (core has no de pack; long text is covered geometrically).
- `.dialog-beside-chrome` is not exercised, because #6125 is unmerged and the class does not exist on main.

## Pre-existing, out of scope

- The fa locale lacks `common.confirm`, so the OK button reads "Confirm" in English. Weblate and the fa pack should supply it; this change adds no new key.
- At phone width the question has no scrim of its own, so dialog content shows around it. That was already the case, at 1024 too.

Pre-flight: review record — updated; web/help topic — n/a (no new control or step; where a confirm question sits on screen is not documented); docs-shots — surface hash refreshed, no pixel change (no shot shows this state); reference/ — n/a (the optional `list-and-dialog-pattern.md` line was not needed: the pattern "the head's Lock stays reachable" is unchanged); README — n/a; en.json/locales — n/a (no strings); follow-ups — none new (the fa `common.confirm` gap belongs to the pack/Weblate).

## Verdict

Changes made (finding 1 fixed and tested); approve with that fix.
