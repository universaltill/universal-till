# 2026-08-06 — htmx settle-window click drop (ut-docs#239)

## What shipped

Vendored htmx 1.9.12 defers binding event listeners on freshly-swapped
DOM content into its internal "settle" phase, which only runs after
`htmx.config.defaultSettleDelay` (20ms by default, via `setTimeout`). A
real click landing in that window — e.g. tapping a basket line's ✕ right
after a scan swaps `#basket` via `hx-swap="outerHTML"` — was silently
dropped: no request fired, no error, the customer's input just vanished.
Confirmed by reading the real vendored source, not just the symptom:
`insertNodesBefore` pushes the listener-binding step (`Oe`, which calls
`htmx.process`) onto a `tasks` array that only runs inside the settle
function `s`, which is itself gated by
`if (v.settleDelay > 0) { setTimeout(s, v.settleDelay) } else { s() }`.

**Fix:** `<meta name="htmx-config" content='{"defaultSettleDelay":0}'>`
added to both templates that load htmx (`web/ui/layouts/base.html`,
`web/ui/pages/self_order_shop.html`). htmx applies this meta tag itself,
before it processes `document.body` — so there's zero listener-ordering
dependency and zero risk of throwing if `htmx.min.js` somehow fails to
load. With `settleDelay = 0`, `s()` runs synchronously right after swap,
in the same call stack as DOM insertion — no event-loop yield exists
between insertion and listener binding, so the window is closed
entirely, not just narrowed.

Non-goals (explicit, from BA/Architect): no htmx 1.9.12→2.x version
upgrade (a much larger, offline-first-relevant change to a vendored
asset, unneeded here), no capture-phase click-replay queue (bespoke
input-replay machinery in the checkout path, can double-fire, papers
over the window instead of removing it).

## Tests

- **New, deterministic regression test**
  (`e2e/tests/sale-screen-213.spec.ts`, "a click landing in the htmx
  settle window on freshly-swapped #basket is not dropped (ut-docs#239)"):
  races a synthetic click — scheduled via `setTimeout(fn, 0)` from inside
  a `htmx:afterSwap` listener — directly against htmx's own internal
  settle timer. A real Playwright `.click()` goes through a CDP round
  trip too slow to reliably land inside the original ~20ms window even
  pre-fix, so it can't prove this on its own; the synthetic click can.
  TDD: confirmed RED pre-fix (real failure: no `/api/pos/remove` request
  seen), GREEN post-fix.
- Removed the old `waitForFunction` poll-for-`htmx-internal-data`
  workaround from the existing "count badge tracks add, remove and
  clear" test (no longer needed) — but see review finding F1 below on
  what that test's un-waited click does and doesn't prove.

## Independent review (fresh-context Opus subagent, medium-complexity card)

**Verdict: safe to merge, no blockers.**

What it actually ran, not just read:
- Read the real vendored `htmx.min.js` and independently confirmed the
  claimed mechanism (`Oe`/`zt`/settle-gate) matches the fix's reasoning.
- Empirically verified `meta[name="htmx-config"]` ordering in a real
  browser against a real running server on 4 pages (`/`, `/self-order/shop`,
  `/settings`, `/catalog`) — `htmx.config.defaultSettleDelay === 0` on
  every one, zero console errors.
- **Independently re-verified the TDD claim itself**, not on our word:
  reverted just the two template changes (kept the new test), killed the
  stale e2e server first (templates are `go:embed`ed — a `reuseExistingServer`
  stale binary would have silently served pre-change HTML), reran — RED,
  exactly the new test failing with the claimed symptom. Restored the
  fix, reran — GREEN. Repeated 6× for flake: 42/42 passed.
- Grepped the whole repo (outside `web/public/vendor/`) for anything
  depending on the 20ms settle timing (CSS transitions, swap-spec
  modifiers) — none found.
- Confirmed both, and only both, templates that load `htmx.min.js` got
  the fix.
- Ran the full standard gate (`go build`, `go vet`, both guards,
  `go test ./...`, full e2e both projects) — all clean except the known
  pre-existing `internal/issuereport` sandboxed-root-run flake
  (ut-docs#258) and one e2e failure (`catalog-image-to-till.spec.ts`)
  that it independently reproduced identically on clean `main` before
  concluding it was pre-existing and unrelated.

**Findings — fixed:**
- **F1 (should-fix, comment accuracy):** the in-file comment on the
  existing "count badge tracks add, remove and clear" test claimed its
  un-waited click was "the regression guard" for #239. The reviewer
  demonstrated that assertion still passes with the fix fully reverted —
  a real Playwright click is too slow to reliably land inside the
  original window, so that test provides no #239 signal on its own.
  **Fixed:** reworded the comment to say plainly that the dedicated new
  test is the real guard, and this one only lost a now-unneeded
  workaround — exactly the "false-pass regression check" failure class
  this pipeline has shipped before and now explicitly guards against
  misleading a future maintainer into weakening the real guard.
- **F2 (nit, adopted):** switched the mechanism itself from an inline
  `DOMContentLoaded` listener referencing `htmx.config` directly to
  `meta[name="htmx-config"]` — htmx's own documented mechanism, applied
  before it processes the body, no listener-ordering dependency, and no
  `ReferenceError` risk if `htmx.min.js` ever fails to load. Verified
  green again after the switch (full `sale-screen-213.spec.ts` rerun,
  7/7 passed) and reran the full gate.
- F3/F4 (nits): both were side effects of switching to the meta tag
  (F2), so adopting F2 resolved them too.

**Deferred, out of scope, new Backlog card recommended:** the reviewer's
step-5 sweep ("did every htmx-loading page get the fix") surfaced that
`web/ui/pages/setup.html` has an `hx-post="/api/setup/join"` form but
never loads `htmx.min.js` at all — pre-existing on `main`, unrelated to
this change, looks like the first-boot wizard's "join an existing till"
step silently falls back to a plain browser form submit. Filing as a
new Backlog card rather than folding into this diff.

## Verified beyond automated tests

- Manual driven check: started the app, scanned an item, screenshotted
  the sale screen — basket renders normally, no visual change (this fix
  touches zero CSS/layout, so no visual regression was expected or
  found).
- Confirmed via the review's independent browser check that
  `defaultSettleDelay` is actually 0 at runtime on every page that loads
  htmx, not just in the template source.

## Safe to merge

Yes. Diff touches zero Go/`internal/` code (repository-pattern, money,
plugin-verification rules genuinely N/A here — confirmed, not assumed:
no SQL text, no monetary handling, no plugin code). No new user-facing
strings (i18n guard agrees). No real client/shop names, no secret-shaped
literals.
