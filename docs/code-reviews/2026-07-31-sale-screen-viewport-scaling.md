# Code review — sale screen viewport-responsive scaling (2026-07-31)

**Branch:** `fix/sale-screen-viewport-scaling` (ut-docs#161)
**Scope:** `web/public/app.css`, `internal/httpx/httpx.go`, `internal/httpx/template_helpers_test.go`, `web/ui/layouts/base.html`, `e2e/tests/tender-panel-reachable.spec.ts`.

## What shipped

The sale screen previously capped at 1500px (dead gutters on any wider till
display) and had a static `html{font-size:17px}` baseline from the earlier
Phase A (#144) 10-inch bump — no viewport-responsive scaling at all.

- `body.sale-screen .container { max-width: none; }` — sale screen fills the
  full viewport width; every other page (settings/reports/catalog) keeps the
  existing 1500px reading-width cap.
- `--fluid-fs: clamp(17px, min(13.57px + 0.335vw, 13.25px + 0.625vh), 20px)`
  drives `html`'s font-size — a two-point linear fit in *both* width and
  height (`min()` of the two), anchored so the four required resolutions
  (1024×600 → 1280×800 → 1366×768 → 1920×1080) land at 17px → 17.86px →
  18.05px → 20px. Nearly everything else in the sale screen is already
  `rem`-based, so this one lever cascades the whole UI.
- `.grid` tile minmax (150px→8.8rem) and `.thumb` height (72px→4.25rem)
  converted from `px` to `rem` so they participate in the same scaling
  instead of staying frozen.
- Kiosk-mode `.btn`/`.btn-tile` no longer set a *smaller* min-height than
  non-kiosk (was 2.75rem/5.4rem vs 3rem/8.5rem) — a real, pre-existing bug:
  kiosk is where actual touchscreens live, and the smaller sizes computed
  under 48px at common resolutions, violating this card's own touch-target
  floor. Kiosk now inherits the base (larger) sizes.
- `internal/httpx`: `uiScalePx` (existing, tested, still used by the 4
  standalone screens — login/setup/self_order/self_order_shop) is untouched.
  New `uiScaleCSS`/`uiscale` template func exposes the *raw* clamped
  UI-scale multiplier (not pre-multiplied by 16px) so `base.html`'s `<html>`
  can carry it as a CSS custom property (`--ui-scale`) instead of a final
  computed pixel value, letting `html{font-size:calc(var(--ui-scale,1) *
  var(--fluid-fs))}` compose the operator's existing manual scale setting
  with the new automatic viewport fit.
- `.tab-panel` gets a `min-height: 6rem` floor (was `0`), mirroring
  `.basket-scroll`'s own existing floor for the identical failure class.

TDD: `TestUIScaleCSSClampsToRawMultiplier` written first, confirmed failing
(`undefined: uiScaleCSS`) before implementing.

## Why this needed two extra rounds — a genuinely wrong first design, caught live

The **first** implementation was CSS-only: a plain `html{font-size:clamp(...)}`
rule. Real browser verification (not just unit tests) found it was
completely inert — `base.html`'s `<html>` tag unconditionally rendered a
server-computed inline `style="font-size: NNpx"`, and an inline style always
wins over any external stylesheet rule, `clamp()` or not. This meant even
the *old*, pre-this-ticket static 17px rule had been dead in the running app
the whole time. Fixed by moving the operator's UI-scale multiplier into a
CSS custom property instead of a final px value, so `calc()` in the
stylesheet can do the composition CSS-side.

## Independent review (different model: Opus) — two real, serious findings, both fixed and re-verified

**Round 1 finding (blocking):** the fluid baseline scaled on viewport
**width only**. On a wide-but-short screen (reviewer's repro: 1920×800,
default scale), every `rem` inflated without regard to the tender panel's
fixed vertical budget, and `.pos-container > .tender { overflow: hidden }`
clipped Cash/Card/Gift Card/Hold Sale/New Customer off screen — worst at
1920×1200 kiosk scale 2.0 (the field-reported device class), where nothing
below the tab bar rendered at all.

Fix: `--fluid-fs` became `min()` of a width curve and a height curve (same
AC-anchored two-point fit, this time against height), and `.tender` lost its
`overflow: hidden` in favor of the same `overflow-y: auto` fallback
`.basket` already uses for an identical documented flex-collapse class.

**Round 2 finding (blocking, and a methodology bug in my own re-verification):**
the `overflow-y: auto` change on `.tender` didn't just relieve the clip — it
exposed a *worse* failure. `.tab-panel` (`flex: 1; min-height: 0`) collapsed
to a real, hit-testable 0 `clientHeight` once its ancestor became a scroll
container, so the payment buttons rendered *nowhere*, not merely clipped.
My first re-verification pass used bounding-box / `scrollIntoViewIfNeeded` /
`isVisible` assertions — the reviewer proved all three return `true` for an
element sitting inside a zero-height overflow container, i.e. a false-pass
of the exact shape this pipeline has hit before (the AI-camera test). Fixed
by giving `.tab-panel` the same `min-height: 6rem` floor `.basket-scroll`
already has, then re-verifying with real hit-testing (`elementFromPoint`
identity plus an actual `.click()` that completes a real Cash sale end to
end) instead of geometry.

**Bonus finding**: the fixed `.tab-panel` floor also closes a *pre-existing*
production bug on `main` — Cash/Card were already unreachable at plain
1024×600, default UI scale (an AC resolution, no manual scale involved),
confirmed by the reviewer rebuilding baseline and hit-testing it directly.

The reviewer independently rebuilt from scratch both rounds (confirming
served CSS by md5 against the on-disk diff each time — flagging along the
way that my own rebuild-and-restart script was serving stale content from a
leftover process still bound to the test port, a real methodology trap
worth remembering), re-ran the full gate itself, and re-derived every
verdict rather than trusting the description.

**Left for follow-up (non-blocking, noted for a future card):**
- Gift Card now sits below the tab panel's fold at default scale on several
  resolutions where baseline showed it directly — reachable by scrolling,
  but touch tills have no visible scrollbar, so it's a discoverability
  regression worth a scroll affordance or tighter grid later.
- `.basket-scroll` still drops from 2 visible sale lines to 1 at 1920×1080
  default scale (found in round 1, unrelated to the tender fix, unaddressed).
- `.grid` tile minimum and kiosk tile sizing both now scale with
  `--ui-scale` too (multiplicatively larger at high manual scale) — expected
  given the composition, flagged as worth calling out rather than a defect.
- `var(--ui-scale, 1)`'s fallback only guards *absence*, not a garbage
  value — low risk since the value is server-generated, not user-editable.
- Printed receipts (`web/ui/partials/receipt.html`) are rem-based inside a
  fixed-px wrap; root now starts at ≥17px instead of 16px, so receipt line
  wrapping may shift slightly — not testable without real printer hardware.

## Verification

- `go build ./...`, `go vet ./...` clean (both sides, every round).
- `go test ./...` clean except `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`) — confirmed pre-existing and unrelated by running
  it against unmodified `main` (fails identically there; a sandbox
  root-permission artifact already documented in PRs #119/#122/#123).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  both pass.
- Full existing Playwright e2e suite (23 specs, default project, real
  Chromium, including the new `tender-panel-reachable.spec.ts`): 22 passed,
  1 pre-existing unrelated failure (`catalog-image-to-till.spec.ts`'s image
  assertion — reviewer re-confirmed it also fails on baseline `main`).
- Custom 34-case matrix (2 modes × 4 built-in themes × 4 required
  resolutions): no horizontal overflow, sale screen fills full width, every
  visible `.btn`/`.btn-tile` ≥48px effective height, root font-size
  genuinely fluid (16.5–20.5px range), non-sale-screen pages keep their
  1500px cap — all pass, screenshots visually spot-checked.
- Real hit-test verification (not geometry) at the reviewer's exact
  4-config failure matrix plus two new permanent specs covering the
  1024×600-default-scale and wide-short-high-manual-scale classes: every
  payment/footer button is the genuine `elementFromPoint` target, and a
  real click completes an actual sale end to end, at every config including
  the worst case (kiosk 1920×1200, scale 2.0).

## Verdict

**Safe to merge.** Two real, independently-caught regressions along the
way — both fixed at the actual root cause (not papered over), re-verified
with stricter methodology than the round that missed the second one, and
now guarded by a permanent regression test committed alongside the fix.
