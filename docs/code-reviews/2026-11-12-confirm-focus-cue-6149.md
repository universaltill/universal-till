# Review: focus cue on the till confirm dialog (ut-docs#6149)

**Change.** The till's in-page confirm `#ut-confirm` (`web/public/till-confirm.js`, ut-docs#5524) already moves focus to one button: Cancel for a danger question, OK for a neutral one. After a tap or mouse open, though, nothing showed which button had focus, because the browser's `:focus-visible` heuristics draw no ring after a pointer interaction.
- `web/public/app.css`: one new rule, `#ut-confirm .modifier-actions .btn:focus { outline: 2px solid var(--focus-border); outline-offset: 2px; }`. It uses `:focus` on purpose, not `:focus-visible`, so the "Enter goes here" cue shows for tap, click and key opens alike. The declaration and the token are the same as the other `--focus-border` rings, and every theme defines that token at ≥3:1.
- New `e2e/tests/confirm-focus-cue-6149.spec.ts` covers 2 themes (default and dark) × 2 open paths (tap and key) at 360×640 with `hasTouch`. Each case asserts that exactly one `.btn:focus`, a non-`none` outline at least 2px wide, and ≥3:1 contrast against the dialog's effective background. It also attaches a screenshot.

**Pipeline.** Dev was Sonnet and TDD-first. A separate Tester agent re-verified red and green: with the CSS hunk stashed, 4/4 fail on `outline-width 1 < 2`; with it restored, 4/4 pass. The Reviewer was an independent reviewer (different model from the author).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | medium | `guard-docs-shots.sh` fails because `web/public/app.css` is part of the hashed screenshot surface. The ring paints only while the dialog is open and focused, and no docs-shot captures `#ut-confirm`, so no help pixel changes. | **To do at commit**: run `scripts/ci/update-docs-shots-surface-hash.sh` and commit with a `Docs-Shots-Unchanged: true` trailer, as #5524 did. |
| L1 | low | The spec's contrast helper keeps the first three channels of `rgba()` and drops alpha, for both the ring and the background. A translucent token would make it overstate contrast. Today `--focus-border` and the dialog surface are opaque in both tested themes, so there is no false pass. | Accepted. |
| L2 | low | Only the default and dark themes are driven. fresh, amber, monarch and monochrome rely on their documented `--focus-border` ≥3:1 values (ut-docs#797, #2176). | Accepted, because the token is already contrast-audited per theme. |
| I1 | info | Using `:focus` instead of `:focus-visible` leaves no ring stuck on after a click. Cancel and an armed Confirm close the dialog and focus returns to the opener. The one case that keeps the dialog open is a Confirm tap inside the 350 ms arm window. That leaves the ring on Confirm, which is correct, because Enter now goes there. | No change. |
| I2 | info | The spec checks that one button is focused but not which one. The ut-docs#5524 spec already pins the danger→Cancel and neutral→OK choice. | No change. |

Specificity is fine. No existing `.btn:focus`, `.btn:focus-visible` or theme `outline` rule competes, and `.modifier-modal`'s padding leaves room for the 2px offset ring inside the dialog's `overflow-y: auto` box. The Tester's screenshots confirm the ring is not clipped. Nothing changes for RTL, since `outline` has no direction. The spec's probe uses logical `inset-*`.

## Verification
- The new spec ran once in the reviewer pass with `--project=default`: **4 passed** (28 s).
- Guards run from the repo root: `guard-rtl-logical-css`, `guard-i18n`, `guard-fixed-dates`, `guard-no-showmodal` and `guard-no-native-confirm` all pass (exit 0). `guard-docs-shots` fails; see M1. The change adds no strings, no dates, no `showModal` and no `window.confirm`.
- The Tester visually checked 4 screenshots (light and dark × tap and key, 360 px). Each shows a 2px ring on Cancel, green in light and teal in dark, intact and not clipped.
- Touch was **emulated only** (Playwright `hasTouch` and `.tap()`); no real hardware was used. RTL, German, large-tablet and kiosk sizes were not driven.

Pre-flight: web/help topic n/a (no topic describes the dialog's focus state; screenshots unchanged → surface-hash refresh + `Docs-Shots-Unchanged`, M1); reference page n/a (ux pattern unchanged, the `--focus-border` ring is the existing convention); en.json/meta n/a (no strings); README n/a; review record updated (this file).

**Verdict:** approve, once M1's surface-hash refresh is in the commit.

**Deferred:** none new. ut-docs#6118 (phone Lock under the scrim) remains open from #5524.
