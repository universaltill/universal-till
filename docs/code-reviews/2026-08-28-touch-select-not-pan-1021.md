# ut-docs#1021 — touchscreen drags select text instead of panning

**Branch:** `fix/1021-touch-drag-selects-not-pans` · **Date:** 2026-08-28

## What shipped

`web/public/app.css`: the operator surface is now non-selectable —
`user-select: none` (+`-webkit-`) on `body`, re-enabled for
`input, textarea, [contenteditable], code, pre`. Widens ut-docs#1170's
`.btn`-only protection to the whole surface. Plus
`e2e/tests/touch-select-pan-1021.spec.ts` (computed-style regression
guards) and regenerated help screenshots (guard-docs-shots).

## Root cause — established on the physical till, layer by layer

The report was "cannot scroll anywhere on the touchscreen; dragging
highlights text" (Pi 5, WebKitGTK 2.52.6, labwc 0.9.8/Wayland, 10.1"
multitouch panel). Every layer below the page was proven healthy first:

| Layer | Verdict | Method |
|---|---|---|
| Panel/kernel | multitouch OK | `/proc/bus/input/devices`: protocol-B MT axes, BTN_TOUCH, INPUT_PROP_DIRECT |
| libinput | touch OK | `libinput list-devices`: `cap:t`, seat0 |
| Compositor | native touch delivered | raw Wayland client (`wev`): `wl_touch.down/motion/up` frames |
| GTK3 | kinetic pan works | plain `GtkScrolledWindow` probe scrolled 72 steps from a synthetic swipe |
| WebKit shell | pan works on plain pages | served a bare tall page on :8080; the UNPATCHED shell panned it kinetically (server-verified scroll telemetry) |
| **Till pages** | **selection instead of pan** | same synthetic gesture on `/settings` |

The discriminating experiment: an **upward** drag (which can scroll) pans;
a **downward drag at the top of the page** — nothing to pan in that
direction — falls through WebKit's gesture arbitration into a mouse-style
text-selection drag (reproduced deterministically with synthetic evdev
gestures; screenshot shows selection painted across the payments card).
Once a selection exists, subsequent drags extend it rather than pan, which
is why the operator experiences *no scrolling anywhere*. A real finger hits
this instantly (first gesture at page top is commonly downward), which is
why the product owner could reproduce at will while "clean" synthetic
up-swipes panned fine.

## Device verification of the fix

Applied to the served CSS on the Pi first: down-drag-at-top → **no
selection highlights**; up-drag → **pans** (screenshots at both steps).
Then implemented properly in the repo.

## TDD / false-pass verification (author's own)

Both new tests fail against unfixed CSS (`userSelect` computes `auto` on
body) and pass with the fix — verified by stash/rerun/restore. HONESTY
NOTE per the #1170 pattern: Playwright/Chromium cannot reproduce the
WebKitGTK pan-vs-select arbitration, so the behavioural proof is the
device evidence above; the e2e tests pin the CSS scoping only.

## Gates

- `guard-i18n` / `guard-compliance-claims` / `guard-docs-shots` (after
  `make docs-shots`): pass. `gofmt` clean; no Go changes.
- Full e2e suite: 209 passed + 1 failure `settings-fee-row-251` —
  **pre-existing**: fails identically in-suite on a clean tree at the same
  base, passes standalone. Filed as ut-docs#1200 (cross-spec server-state
  interference), not caused or worsened here.

## Independent review (Opus, isolated worktree, different model from author)

Ran independently in an isolated worktree; re-ran every gate itself
(gofmt/build/i18n/docs-shots/compliance all green) and personally
re-verified the TDD claim — reverted app.css (with a server rebuild, since
web/ is go:embed-ed), saw BOTH tests fail on the primary assertion
(`Expected: "none" · Received: "auto"`), restored, saw green. Also probed
computed user-select on 8 live pages, checked kiosk-scoping false-pass risk
two ways (config + empirical), grepped for selection/clipboard consumers
(zero in the repo), and audited the manual (no topic instructs selecting
text; screenshots regenerated and guard-verified).

Verdict: **safe to merge**; two should-fix findings, both applied before
merge:

1. **Customer order-tracking page wrongly caught** — `order_tracking.html`
   is "an anonymous customer on their own phone, not an operator" by its
   own header (ut-docs#527) and loads app.css standalone, so the new body
   rule made a customer's receipt number unselectable on their own phone.
   Fixed: `body.tracking-screen` re-enables selection.
2. **The `<code>` exemption re-opened the failure mode on the exact
   verification page** — /settings renders store/device IDs as `<code>` at
   the very top, so a down-drag starting on one could still fall into a
   selection drag; the device verification missed it only by touch
   coordinate. Fixed: `code, pre` selectability now gated behind
   `@media (pointer: fine)` — desktops keep copyability, coarse-pointer
   tills have zero selectable text outside inputs.

Recorded, no action needed: the manual pages are now unselectable (real but
acceptable cost — no copy affordance existed anywhere, and no help topic
relies on selecting text); `.bugreport-close`'s `user-select: auto` comment
was already a no-op pre-change (the `auto` keyword is not an escape hatch
under a `none` ancestor — future reader beware); `[contenteditable]`
currently matches nothing (future-proofing); the inline
`user-select:all` enrol-code chip (sync_api.go) survives via inline-style
precedence. Test-quality: the reviewer's one improvement (make the `<code>`
assertion loud instead of skippable) was applied; the kiosk-scoping
false-pass check confirmed the body assertion is load-bearing on non-kiosk
pages. A stale same-card branch with zero unique commits was deleted to
avoid the in-flight-duplication pattern.

## Deferred / adjacent

- ut-docs#1194 (designer tile-reorder via HTML5 DnD on touch) — separate,
  still open.
- ut-docs#1199 (shell-vs-service boot race / split-brain DB) — found
  during this investigation, own card.
- ut-docs#1093 gained a new observation (warm-relaunch corruption;
  python-gi clean control case) — commented there for the upstream report.
