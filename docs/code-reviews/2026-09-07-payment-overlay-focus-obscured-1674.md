# Code review: three more payment-overlay controls drop out of tab order while covered (ut-docs#1674)

**Branch:** `fix/1674-payment-overlay-focus-obscured`
**Card:** universaltill/ut-docs#1674 (found reviewing #1629, complexity:medium)
**Reviewer:** independent Opus subagent, isolated worktree (Sonnet wrote the fix)

## What shipped

`#payment-overlay` opens non-modally (`.show()`, ut-docs#1385), so nothing
outside it becomes `inert`. #1629 gave two controls (`kiosk-checkout-start`,
`tender-footer-hold`) `tabindex="-1"` while the open overlay geometrically
covers them, via a `targets` array in `web/public/app.js`'s
`updateFocusability()`. #1629's own independent review swept 15 viewport
widths and found three more controls in the identical state — WCAG 2.2
SC 2.4.11 (Focus Not Obscured) — that were out of that card's scope:

- **`.tender-quickpay`'s one-tap charge button** (`data-testid="quick-pay"`)
  — the most urgent: unlike the overlay's own trigger, activating it POSTs
  `/api/pos/tender` directly, so a keyboard operator could complete a
  charge on a control they cannot see. Covered 901–1500px.
- **The Payment trigger itself** (`data-testid="payment-open"`) — measured
  covered at **every** width tested, 901–1920px, unconditionally.
- **The phone-width New Sale duplicate** (`kiosk-checkout-start-phone`) —
  same latent issue at ≤480px, where the overlay goes full-screen.

This card extends the exact same, already-reviewed mechanism to those
three — no new coverage/restore logic, three more entries in `targets`:

```js
var targets = [
  footer.querySelector('[data-testid="kiosk-checkout-start"]'),
  footer.querySelector('[data-testid="tender-footer-hold"]'),
  footer.querySelector('[data-testid="payment-open"]'),
  document.querySelector('[data-testid="quick-pay"]'),
  document.querySelector('[data-testid="kiosk-checkout-start-phone"]'),
].filter(Boolean);
```

`quick-pay` and the phone duplicate live outside `.tender-default-footer`
(`.tender-quickpay` and `.kiosk-header.phone-fallback-only` respectively),
so they're queried from `document` rather than `footer`. `payment-open` is
inside the footer like the original two. The stale comment claiming these
three were "deliberately excluded... not in scope" was updated to explain
why they're now included and why quick-pay is the most urgent.

New e2e spec `payment-overlay-focus-obscured-1674.spec.ts`, three cases:
quick-pay (covered at 1024×600, not covered at 1920×1080 — negative
control), Payment trigger (covered at both 1024×600 and 1920×1080, since
the review measured it as covered at *every* tested width — no negative
control exists in that range, so the spec documents that explicitly rather
than inventing one, and also asserts the trigger's own `.click()` still
opens the overlay despite `tabindex="-1"`, since tabindex only removes
keyboard-Tab reachability, not programmatic activation), and the phone
duplicate (covered at 375×667 where the overlay goes full-screen, and
confirmed the existing `r.width === 0 && r.height === 0` not-rendered
guard still correctly no-ops it at 1024×600 where `display:none` applies).

`make docs-shots` regenerated two screenshots (`till-designer` ar,
`multitill` fa) because the guard's content hash covers all of
`web/public/**`, which this change touched — decoded and pixel-diffed by
the reviewer, confirmed as antialiasing jitter (0.0068% of pixels / max
channel delta 25 on one; 2 pixels / max delta 1 on the other), not a real
change, on two screens that don't even involve the payment overlay.

## Independent review — findings

**Verdict: PASS, safe to merge.** No blocking findings.

The reviewer did not trust the prescribed regression set alone: a `grep`
for every spec referencing `quick-pay`/`payment-open`/
`kiosk-checkout-start-phone`/`tabindex` found 21 specs, including
`phone-width-layout-413`, `rtl`, and `tender-panel-reachable` (which
drives quick-pay at RTL/360px/1280x800) — broadened the run to all 21 and
their dependents, **92 passed**.

Checks made and confirmed correct, beyond reading the diff:

- **No `document.querySelector` duplicate-match risk.** `quick-pay`
  renders twice in the template (`{{ if .defaultPayMethod }}`/`{{ else }}`)
  but the branches are mutually exclusive server-side — confirmed at
  runtime, not just by reading the template, since Playwright's strict
  mode would have thrown on 2+ matches and it didn't.
- **No stale-element risk.** Every target's own `hx-swap` replaces
  `#basket`, a sibling partial — never the targets themselves. `app.js`
  loads with `defer`, so the DOM is parsed before the IIFE captures
  `targets`.
- **`payment-open` doesn't strand focus** when it gets `tabindex="-1"`
  while covered: `dialog.show()`'s native focusing steps move focus to
  `payment-close` inside the overlay. Verified live, not assumed.
- **The `payment-open` negative control is sound as written** — it really
  is covered at every tested desktop width (rightmost button under a
  right-anchored panel), so the spec's real negative control is the
  close-path assertion (`isCoveredByOverlay()`'s `!overlay.open` branch),
  not an invented uncovered viewport.
- **The two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`)
  are N/A here** — confirmed, not assumed: zero `.go` files in the diff,
  and the new spec does no file I/O at all.
- No secret-shaped literal, no real client/shop name.
- `web/public/app.css` has zero `tabindex` references — this change has
  no visual effect, confirmed rather than assumed. No new user-facing
  strings (`guard-i18n.sh` agrees). RTL unaffected by construction (the
  mechanism reads DOM rects, not physical left/right) — `rtl.spec.ts` and
  the RTL cases in `tender-panel-reachable`/`-1542` all pass.
- **No `web/help/` update needed** — tab-order only, nothing a shop owner
  sees or does changes, same as #1629's own finding.

**Two non-blocking nits, noted only, not fixed:**

- `web/public/app.js`'s `if (!footer) return;` gates the whole IIFE,
  including the two targets now queried from `document` (quick-pay, phone
  duplicate) which don't actually depend on `.tender-default-footer`
  existing. Purely theoretical today — all three always render together
  in `index.html` — but it's latent coupling this change introduces.
- `e2e/tests-docs/lib.js`'s generated `algorithm` string describes the
  hashed fileset as `web/ui/** + internal/pages/**.go` only, omitting
  `web/public/**` even though the implementation (and
  `guard-docs-shots.sh`) both actually include it — pre-existing, not
  introduced here, but it's exactly what makes this diff's screenshot
  regeneration look unexplained to a reviewer at first glance.

**Deferred as a new Backlog card, not fixed here (ut-docs#1702):** with
this fix applied, the reviewer measured 11 (1024×600) / 8 (1280×800) / 4
(1920×1080) *other* focusable controls on the sale screen still covered
by the open overlay and still reachable — same defect class, none in any
`targets` array. A hand-maintained list doesn't generalize; the clean fix
is a geometry-driven sweep reusing the same `isCoveredByOverlay()` over
every focusable element outside the overlay, rather than another
hardcoded entry. Correctly out of scope for this card, which follows the
#1629 precedent faithfully for the 3 controls it was asked to fix.

## Verified beyond automated tests

- **TDD, independently re-verified in an isolated worktree** (not taken on
  the implementer's word): killed the reused e2e servers first (the app is
  built once per `run-till.sh` launch and `web/public` isn't hot-reloaded,
  so a stale server would have made the revert invisible), reverted
  `web/public/app.js` only, confirmed all 3 new tests fail on the exact
  claimed assertion (`tabindex` expected `"-1"`, received `""`/`null`) at
  quick-pay/payment-open/phone-duplicate respectively, then restored and
  confirmed all pass again.
- Full regression set green: the 6 specs this surface's own history
  named, broadened to all 21 that actually reference these testids or
  `tabindex` plus their dependents — **92/92 passed**.
- `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), full `go test ./...` — all green
  (no Go files touched by this change; run for completeness per the
  standing gate).
- Guards: `guard-i18n.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-docs-shots.sh` (regenerated, pixel-diffed as above),
  `guard-compliance-claims.sh`, `guard-help-topics.sh`, plus every other
  CI-blocking guard in the `build` job — all green.
- RTL: confirmed unaffected by construction plus a live spec run, not just
  reasoned about.
- No real client/shop name, no secret-shaped literal introduced.
- Manual (`web/help/`): correctly not updated — tab order only, nothing a
  sighted shop owner sees or does.

## Safe to merge

Yes.

## Deferred (new Backlog card)

- ut-docs#1702 — a geometry-driven sweep to replace the hand-maintained
  `targets` list, covering the 11/8/4 other controls the overlay also
  covers at various widths that this card and its predecessors never
  listed.
