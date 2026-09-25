# Review — receipt auto-reset no longer reloads the page (ut-docs#2763)

Date: 2026-09-25 · complexity:easy (built by Opus 5.5, reviewed by Sonnet 5 in a fresh context)

## What shipped

- `web/ui/partials/receipt.html`: the 60 s auto-reset was an uncancelled `setTimeout(function(){ window.location='/'; }, 60000)`
  — nothing ever cleared it, so the whole sell screen reloaded 60 s after every sale, usually mid-way through the next one
  (the reported "screen refreshes every few minutes"). It's replaced by a single-handle timer (`window.UT.receiptReset`) that
  fires `htmx.ajax('GET', '/ui/basket', { target: '#basket', swap: 'outerHTML' })` instead of navigating, and is cancelled by:
  any htmx request/swap aimed at `#basket` (`htmx:beforeRequest`/`htmx:beforeSwap`, checked via `target === receipt ||
  target.contains(receipt)`), a later receipt's own script re-running, or the app shell's own navigation swapping `#ut-page`.
  A `pointerdown` on the receipt restarts the minute instead of resetting under the cashier's finger.
- "New Customer" changed from `onclick="window.location='/'"` to `hx-get="/ui/basket" hx-target="#basket" hx-swap="outerHTML"
  hx-sync="#basket:replace"` — same pattern as every other `#basket`-targeting control in this app.
- New spec `e2e/tests/receipt-auto-reset-no-reload-2763.spec.ts` (5 tests, fake timers via `page.clock`), detecting a reload
  via a `window.__ut2763` marker that only a real navigation destroys.

## Verified

- `/ui/basket` (`internal/pages/basket_page.go`) calls `Engine.Scan("")`; traced `scanQty("", …)` — every `code != ""` branch
  is skipped, it falls straight to `recomputeTotals()` and returns the existing basket unmutated. Genuinely side-effect-free,
  and an already-established route (`index.html`'s own `hx-trigger="load"` fetch uses it identically).
- The receipt's own top element carries `id="basket"` (`pos_api.go:2138/2142`), matching `.closest('#basket')` and the
  `outerHTML` swap target — confirmed, not assumed.
- No `hx-preserve` anywhere in the touched templates — the OOB-self-swap trap doesn't apply here.
- Traced `base.html`'s page-scoped-listener monkey-patch (`EventTarget.prototype.addEventListener`, only auto-drops
  `window`/`document`/`body` listeners on a genuine `#ut-page` shell swap, i.e. `hx-boost`-implicit navigations like the nav
  rail — never on a same-page `#basket` swap). The comment "base.html drops them on navigation" is accurate for that one
  path; for the everyday repeated-sale cycle (thousands/day) the script's own `handle.cancel()` is the sole cleanup, and it's
  correctly wired to every relevant event. No accumulating listeners either way.
- `pointerdown`, `document.currentScript`, `Node.isConnected`, `Element.closest/contains` — all plain ES5/broadly-supported,
  same idiom already used elsewhere in `index.html`/`buttons.html`/`self_order*.html`; no Android WebView/WebView2 risk.
- Refund and Print buttons: untouched.
- Ran the new spec (all 5 pass) and `go test ./internal/pages/ -run 'Basket|Tender|Receipt'` (all pass, incl. voucher/tender
  paths that render receipts).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | On a genuine shell navigation, `base.html`'s `dropPageListeners()` can remove the receipt's document listeners without going through `handle.cancel()`, leaving its `setTimeout` uncleared for up to 60 s. | Not fixed — harmless: `fire()` still runs `receipt.isConnected` guard first and no-ops (the node was removed from `#ut-page` on that same swap), so no stray navigation or fetch results. |
| 2 | nit | The auto-fire's `htmx.ajax()` call has no `hx-sync` group, so a request it dispatches can theoretically race a same-instant cashier action targeting `#basket` (the reverse direction — new actions cancelling the *pending* timer — is fully covered). | Accepted, not fixed: the window is one local-server round-trip, unreachable by human reaction time to an internal timer, and worst case is a transient stale render that a next scan overwrites — categorically smaller than the shipped bug. |

No blocking defects. Both notes are theoretical/bounded, not reachable in the shipped bug's own severity class.

**Verdict: safe to merge.**
