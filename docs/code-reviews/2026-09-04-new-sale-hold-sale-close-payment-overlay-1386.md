# 2026-09-04 — New Sale / Hold Sale close the payment overlay before resetting the basket (ut-docs#1386)

## What shipped

`web/ui/pages/index.html`:

- The `.tender-default-footer` **New Sale** button (`data-testid="kiosk-checkout-start"`,
  `hx-post="/api/pos/reset"`) gained an `hx-on::after-request` handler that
  closes `#payment-overlay` on a genuine success — same pattern already used
  by the pay-grid/quick-pay buttons (check `toast-message` isn't showing an
  inline error, then close only if the dialog is actually open).
- `#hold-modal`'s own `<form>` (posts `/api/pos/hold`) got the identical
  closing logic added to its existing `hx-on::after-request`. This is where
  Hold Sale's actual basket-reset happens — the button in
  `.tender-default-footer` only opens the dialog, it has no `hx-post` of its
  own — so this is the correct place for the fix, not the outer button (the
  issue's own suggested-fix location).

New e2e coverage: `e2e/tests/new-sale-closes-payment-overlay-1386.spec.ts`,
two tests driving the real buttons and asserting `#payment-overlay` actually
closes.

## A real finding beyond this issue's scope, made along the way

While writing the e2e coverage, measured (not assumed) that **Hold Sale is
frequently not a reachable hit target at all while the overlay is open** —
`.payment-overlay` is a fixed 26rem right-anchored panel (`app.css`) that
covers Hold Sale's screen position almost entirely at common desktop
viewports (measured: ~80% covered at 1366x768, ~100% at 1024x600 and at
Playwright's default 1280x720 — center point covered, real click
intercepted). It only becomes reliably clickable from ~1600x900 up,
comfortably so at 1920x1080. New Sale (further left in the same row) is
clearer of the overlay at wide viewports but is *also* significantly
covered at 1024x600.

This is a **separate, pre-existing reachability gap** — this issue's own
scope is specifically "close the overlay after the reset happens", which
assumes the buttons are reachable "at some viewports" to begin with (the
issue's own wording). Filed as ut-docs#1542 rather than silently expanding
this diff or silently leaving it undiscovered. The e2e coverage added here
uses an explicit 1920x1080 viewport for the Hold Sale test specifically
*because* of this finding — a real, unforced click, not `force: true`
(this file's own established standard, see `payment-overlay-osk-1385.spec.ts`'s
comment on why force is never used to paper over an unreachable target).

## TDD verification (done myself, independently)

Reverted the `index.html` fix (`git stash`), re-ran
`new-sale-closes-payment-overlay-1386.spec.ts`: both tests failed exactly as
expected — New Sale's on `expect(...).not.toBeVisible()` timing out with
the overlay still open, Hold Sale's the same, confirming the tests actually
exercise the bug rather than passing vacuously. Restored the fix
(`git stash pop`), re-ran: both green.

## What was run

- `e2e/tests/new-sale-closes-payment-overlay-1386.spec.ts` (new),
  `hold-named-tab.spec.ts`, `payment-overlay-osk-1385.spec.ts`,
  `tender-panel-reachable.spec.ts`, `phone-width-layout-413.spec.ts` — 29/29
  passed, `--project=default` (the auth-off project; this change touches no
  auth-gated surface).
- `gofmt -l .` clean, `go build ./...` and `go test ./...` green (no Go
  source touched).
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-e2e-fixtures-import.sh`
  pass — no new user-facing strings added (pure JS attribute logic reusing
  existing DOM/markup), so no locale or manual changes needed.

## Independent review

Spawned a fresh-context Sonnet subagent (`complexity:easy` → Sonnet reviews
Sonnet's own work) in an isolated worktree. **Verdict: safe to merge.**

Independently re-verified, not taken on faith:

- **TDD revert/restore**, done itself: reverted `index.html` only, both new
  tests failed exactly as claimed (the overlay stayed open); restored,
  both passed; full regression set (29 tests) green.
- **1920x1080 viewport choice**, via its own geometry probe: confirmed Hold
  Sale's center point is covered by the overlay at 1280x720/1366x768
  (~80% coverage at 1366x768, matching this record's own number almost
  exactly) and clear at 1920x1080.
- **The toast-error guard is load-bearing, not defensive-only**: read
  `internal/pages/hold_api.go` directly — all three of `/api/pos/hold`'s
  failure branches (empty basket, marshal error, repo insert error) return
  HTTP 200 with an error-level toast, same as `/api/pos/reset`'s success
  path. `event.detail.successful` alone can't distinguish them; the
  `.error`-class check is the only thing that does, and mirrors an
  existing, already-proven pattern (`app.js`'s split-tender-submit
  overlay-close logic).
- **Other callers of the two endpoints**: only `index.html` references
  either. Found one NOT patched — the phone-width top-bar duplicate New
  Sale button (`kiosk-checkout-start-phone`, `.phone-fallback-only`, shown
  only ≤480px). Verified empirically (real click, 375x700, overlay open):
  at that breakpoint `.payment-overlay` goes full-screen
  (`inset-inline: 0; width: 100%`), so the duplicate button is completely
  covered and unreachable regardless — same reachability-gap class as
  ut-docs#1542, not a functional gap this diff introduces. Left unpatched
  deliberately; noting it here rather than silently.
- E2e locator robustness (`.tender-default-footer` scoping avoids the
  phone-duplicate row, which doesn't duplicate Hold Sale anyway) and all
  guards/build/vet/format, confirmed independently green.

## Verdict

Safe to merge.
