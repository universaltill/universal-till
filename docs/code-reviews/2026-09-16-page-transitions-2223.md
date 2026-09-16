# Review: page & menu transitions "like an app" (ut-docs#2223)

**Card:** universaltill/ut-docs#2223 · **Lane:** `lane:local` · **Branch:** `feat/2223-page-transitions`
**Dev:** Sonnet subagent (complexity:medium) · **Independent review:** Opus subagent in an isolated worktree, on the pre-review snapshot `bff8705c` · **ADR:** ut-docs ADR-0096

## What shipped

Cross-document navigation (tile taps, rail taps, browser back) animates through the browser's own CSS View Transitions API — `@view-transition { navigation: auto }` — as a pure progressive enhancement; an engine without it keeps today's instant swap. No client router, no dependency, ADR-0008 unchanged.

- **Direction encodes depth.** A ~40-line inline `pagereveal` script in `base.html`'s `<head>` reads `navigation.activation`: browser back = pop; otherwise a route-depth heuristic (Sell `/` = 0, Menu hub `/menu` = 1, everything else deeper) — Menu→Reports push, Reports→Menu pop, Sell↔Menu push/pop, lateral = push. Expressed as a view-transition *type* (`:root:active-view-transition-type(back)`) plus `data-nav-dir` on `<html>` (test hook + fallback selector). **A reload skips the transition** (review finding 3).
- **Fixed furniture never moves.** `.nav` (rail), `.statusbar` and `main.container` are named groups (`ut-rail`, `ut-statusbar`, `ut-page`); the rail/statusbar/root images get `animation: none; mix-blend-mode: normal`, their old image `opacity: 0`, and the two furniture groups sit at `z-index: 2` above the sliding page. Only the page slides 4 % + fades over `--ut-motion-ms` (200 ms), transform + opacity only.
- **RTL** mirrors through `--ut-nav-dir: -1` on `[dir="rtl"]` — logical, no `left`/`right`.
- **In-page htmx swaps ease** (150 ms opacity .55→1 on the swapped-in region, `htmx:afterSettle`) **only when the operator caused them** — a `load`/`every` poll (orders list, the customer-facing counter display, the rail's sync/fiscal/diagnostics chips, pairing notice) and `hx-swap="none"` never pulse. `defaultSettleDelay` stays 0; no swap delay.
- **`prefers-reduced-motion: reduce`** disables everything through one wildcard block — including the pre-existing `.menu-tile`/`.tab` transitions that had no guard (EAA, ut-docs#741) — except the two progress spinners (essential motion, WCAG 2.3.3).
- **The opt-in is also inline in `<head>`**, not only in `app.css` — see "Verified on the real hardware": Chrome on the pilot tablet aborted every transition *into* a till page when the opt-in lived only in the 244 K stylesheet.

No user-facing strings, no locale keys, no help-topic change (nothing a shop owner reads changed); `web/help/img/manifest.json` surface hash refreshed (no static pixel changes — verified at 1024×600 and 360 px).

## Tester findings before the independent review (fixed, each with a test proven red→green)

| # | Finding | Fix / test |
|---|---|---|
| T1 | `:root:active-view-transition-type(back) ::view-transition-new(ut-page)` (and the `[data-nav-dir="pop"]` twins) carried a **descendant combinator** — the pseudos originate on `<html>`, so the pop direction could never match. | Compound selectors. `TestAppCSSDirectionSelectorsHaveNoDescendantCombinator`. |
| T2 | `animation: none` on both old and new rail/statusbar/root images under the UA's `mix-blend-mode: plus-lighter` **adds** two opaque images — background brightens for 200 ms. | `mix-blend-mode: normal` (+ review's `opacity: 0` on the old image). Same test. |
| T3 | **Chrome Android 153 aborted every transition into a till page** — "Transition was aborted because of invalid state. ViewTransition opt-in disabled" (Chrome's own console) — because it evaluates the incoming document's opt-in before the render-blocking `app.css` has arrived; the same page as the *outgoing* document was always fine. Bisected on the tablet with static copies of `/menu` served from the till's own origin: scripts + `app.css` present → 0/3; opt-in inline → 3/3. | Inline `<style>@view-transition{navigation:auto}@media (prefers-reduced-motion:reduce){@view-transition{navigation:none}}</style>` in `<head>` ahead of the stylesheet. `TestBaseHTMLCarriesTheViewTransitionOptInInline`. |

Two of the Dev's "environment" claims did not survive the devices and the spec's comments were corrected: `pagereveal` **does** fire on every navigation on Chrome Android and WebKitGTK (the "only once" observation is a property of headless Playwright Chromium, where no cross-document transition runs); and six consecutive real touch taps across transitioned pages all navigated, so the "page becomes un-hit-testable" observation is an automation artifact, not a product defect.

## Independent review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | **Blocker** | The ease fired on *every* `htmx:afterSwap`, including `hx-trigger="load"`/`every 15s` polls — `#orders`, the customer-facing `#counter-orders`, the floor plan, `pending_pairings`, and the rail's `#sync-chip`/`#fiscal-chip`/`#diagnostics-chip` + `#pairing-notice-mount` every 30 s — dimming to 55 % and back on a timer with unchanged content, inside the rail the card says must read as fixed. | **Fixed** — gated on `requestConfig.triggeringEvent` (set by htmx 1.9.12 for click/submit/keyup/custom events, `undefined` for load/every/`htmx.ajax` without an event; verified in the vendored source and live). e2e "a `load`/`every` poll swap never eases; a user swap on the same page does" — fails with the gate removed (12 load pulses caught), passes with it. |
| R2 | should-fix | `hx-swap="none"` (45 sites: whole settings forms, refund, catalog delete) still fires afterSwap on the issuing element — the whole form dimmed on Save. | **Fixed** — `closest('[hx-swap]') === "none"` → no ease. e2e probe test with a `none` and a `innerHTML` button side by side. |
| R3 | should-fix | A reload (pull-to-refresh, 8 `location.reload()` sites, 4 server `HX-Refresh`) has `from === entry` → equal depth → "push": the same page slid in from the side. | **Fixed** — `navigationType === 'reload'` → `skipTransition()`. e2e uses the browser's genuine `activation.navigationType` after `page.reload()`; `TestBaseHTMLPageRevealSkipsReloads`. |
| R4 | should-fix | The spec's two environment notes contradicted each other, and the full e2e suite had not run on the branch. | **Fixed** — notes rewritten to the one established mechanism; full suite runs in CI on the PR (the merge gate). |
| R5 | nit | Forced reflow on every swap (basket, every scan). | **Fixed** — reflow only when an ease is still running. |
| R6 | nit | Old rail/statusbar images merely covered by the new; at ≤480 px a taller old bar would peek out. | **Fixed** — `opacity: 0` on the old root/rail/statusbar images. |
| R7 | nit | `::view-transition-group(ut-page)` kept the UA's 250 ms geometry morph while the slide is 200 ms (Sell `max-width: none` vs Menu `1500px` on a wide desktop shell). | **Fixed** — group duration = `--ut-motion-ms`. |
| R8 | nit | Reduced-motion wildcard froze `#refresh-indicator`/`.animate-spin` mid-frame ("hung"). | **Fixed** — spinners exempted; `TestAppCSSReducedMotionKeepsSpinnersTurning`. No `animationend`/`transitionend` listener elsewhere depends on the zeroed durations (reviewer grepped `web/public/*.js` + templates). |
| R9 | nit | `transitions_test.go` pinned the exact bytes of the unrelated `.tab` rule. | **Fixed** — asserts the block still carries a `transition:`. |
| R10 | nit | OOB-swapped regions (`#items-rail`, `#pos-alert`) ease too; a 150 ms stacking context. | Accepted — no z-index overlap found; intended. |

**Found while fixing R1 — a false-pass in the Dev's own test.** htmx's settle step clones an id-matched *old* element's attributes onto the new one and then restores them, **synchronously right after `htmx:afterSwap`** (settleDelay 0) — so the class added in afterSwap was wiped before the first frame. The ease had **never run once** on `#basket`; the MutationObserver-based test saw the transient add and passed. Traced with a `DOMTokenList.add` hook (add seen, `animationend` never fired, final class `basket`). Fix: apply on `htmx:afterSettle` (same tick, after the restore); every swap-ease test now asserts `animationstart` with `animationName === 'ut-swap-in'`, which the old shape could not.

Clean (reviewer): all `::view-transition-*` selectors are root compounds; `@view-transition` nested in `@media` is valid (css-view-transitions-2, confirmed on Chrome 153 + WebKitGTK 2.52); no duplicate `view-transition-name` in any single document (`.nav`, `.statusbar`, `main.container` each once; login/setup/self-order/tracking don't use `base.html`); group `z-index` valid; inline script/style before `<title>` fine (no CSP); `entry.index < from.index` correct for traverse; `from.url === null` cannot throw; `?lang=`/hash ignored via `pathname`; `data-nav-dir` has no same-document consumer; `detail.target`/`detail.elt` semantics verified in `Ie()`/`ce()`; stuck-class cases harmless; no strings, no focus/touch-target changes, logical properties only; manual needs no change; MkdirAll/cwd-relative N/A.

TDD re-verification (reviewer, in the worktree): inline opt-in removed + descendant space re-inserted → `TestBaseHTMLCarriesTheViewTransitionOptInInline` and `TestAppCSSDirectionSelectorsHaveNoDescendantCombinator` FAIL; restored → PASS.

## Verified on the real hardware

Till built from this branch, served on the LAN (`UT_AUTH=off`, port 8097, demo catalogue); a test-only `vt-measure.js` injected at document start recorded, per navigation, whether `pagereveal` carried a transition, the direction the shipped script chose, reveal time, and rAF frames during the transition. **The measurements below predate the review fixes** (which touched the old-image opacity, the group duration, reload handling and the in-page ease — not the cross-document slide itself).

- **Pilot tablet, TECLAST P50T, Chrome 153, real `adb shell input tap` touches** (Reports tile → rail Menu → rail Sell → rail Menu → Settings tile → rail Menu): transition on **every** hop, direction push/pop/pop/push/push/pop as designed; `finished` at 293–356 ms on menu/reports/settings, 536 ms on the sale screen; reveal (page load before the motion) 188–384 ms. A 5 s `screenrecord` (frames emitted only on display change): **Sell→Menu painted 7 distinct frames at 11–23 ms spacing (~50–60 fps)**; Menu→Sell showed one ~57 ms gap (~3 dropped frames) while the sale screen boots; the rail stayed fixed in every frame with no brightness doubling. Every tap after a transition navigated (6/6).
- **Pi 5, `unitill-desktop` = WebKitGTK 2.52.6** (not the Chromium 152 that merely sits installed — corrected on the card), driven in a real `WebKit2.WebView` on the Pi's own display: transition on every hop, direction correct on all 7 hops; `finished` 212–264 ms on menu/reports/settings, 368–392 ms on the sale screen; **main-thread rAF only 9–23 fps during the transition** — the deferred scripts boot under the animation. Whether the compositor keeps the slide smooth regardless could not be graded: the Wayland recorder caps at ~20 fps (a trivial control page reported 58–60 fps in-page while the recording still showed ~19). This contention is what ut-docs#2224 removes; the lever meanwhile is one variable (`--ut-motion-ms`).
- **Chrome Android reduced-motion**: Android's animator scale is not what Chrome maps to `prefers-reduced-motion` (stayed `false`), so that device check was inconclusive; the Playwright `emulateMedia` tests cover the reduced-motion logic.
- **Windows (WebView2)**: a UTM ARM Windows 11 VM exists on the dev Mac but exposes no SSH/RDP yet — **not tested**; noted on the card for the product owner.
- **Looked at**: `/menu` at 1024×600 and 360 px (static layout unchanged), the tablet frame montages of both directions. **Not looked at**: fa/ar mid-transition frames (the RTL variable is asserted by e2e; the geometry is a sign flip on one `translateX`).

## Gate

`go build ./...`, `go vet ./...`, `gofmt` clean; `guard-data-access`, `guard-i18n`, `guard-htmx-loaded`, `guard-help-topics`, `guard-docs-shots` pass; `go test ./...` green except the four pre-existing local `internal/pages/catalog` image-upload failures (Go 1.27/darwin fixture assumption on untouched `main`, per the 2026-09-16 handoff — CI on Go 1.25 is unaffected); Playwright `page-transitions-2223.spec.ts` 12/12 plus `nav-rail-lock-reachable-1346`, `nav-rail-svg-icons-1423`, `sale-screen-category-tabs-search-418`, `sale-screen-213`, `settings-pos-notice-918`, `sale-rail-orders-1349` — 26/26. Full suite: CI.

## Verdict

Safe to merge once the PR's full `e2e` job is green. Deferred, on the board: ut-docs#2224 (persist the shell — moves the load under the motion and frees the Pi's main thread); the Windows shell check.
