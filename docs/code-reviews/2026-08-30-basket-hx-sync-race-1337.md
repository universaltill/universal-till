# Code review: basket race — add `hx-sync` to every `#basket`-targeting trigger

**Card:** universaltill/ut-docs#1337 (p1, field-reported)
**Repo/branch:** `universal-till`, `fix/1337-basket-hx-sync-race`
**Base:** `origin/main` @ `e822582b3dec9503807673b834ef9d38028f200c`
**Merged with:** `origin/main` @ `50b26abc` (PR #667, ut-docs#1314's basket
item-name column width fix) partway through this PR's lifetime — see
"Concurrent-PR merge conflict" below.

## The bug

Product owner, live report: add items in Dine-in, switch to Takeaway, add
more items, delete some — items sometimes randomly disappear and/or stale
ones reappear.

## Root cause

Every basket-mutating trigger across the sale screen (`web/ui/pages/index.html`,
`web/ui/partials/basket.html`, `web/ui/partials/buttons.html`,
`web/ui/partials/table_picker.html`, `web/ui/partials/suggestions.html`,
`web/ui/partials/modifier_picker.html`) independently `hx-post`s and
`hx-swap="outerHTML"`s the **same** `#basket` element, and none of them
carried `hx-sync`.

htmx resolves an element's `hx-target` to a concrete DOM node **once, at the
moment the request is sent** — not again when the response lands. If two
triggers fire close together, both still targeting the same not-yet-replaced
`#basket` node, and their responses arrive at the browser out of order:

1. Whichever response lands **first** performs a real, visible `outerHTML`
   swap of the shared original node. Fine on its own.
2. That swap detaches the original node from the document. The **other**
   in-flight request's target reference is that same original (now detached)
   node — so when its response finally arrives, applying its swap to a node
   that is no longer in the document has **no visible effect**. Whatever it
   would have shown (a newly-scanned item, a removed line, a table
   assignment, …) never renders, even though the server processed it
   correctly and the basket's real state is right.

If the *older*-fired request's response happens to win the delivery race,
the operator's *later* action's effect is the one that silently never shows
up — exactly the "items sometimes randomly disappear" symptom in the field
report. (The inverse ordering is harmless: the newer response lands first,
shows the complete state, and the stale response arrives later to a
detached node and vanishes without visible effect — which is why the bug is
reported as intermittent rather than constant.)

At least 10 distinct trigger kinds share this one swap target: scan, qty/discount
edit, remove line, Hold Sale, order-type toggle, New Sale (reset), tender/pay
buttons, table-picker select/clear, suggestions-strip add, modifier-picker
submit, New Customer reset.

## The fix

Added `hx-sync="#basket:replace"` to every element that targets `#basket`
(htmx's documented mechanism for "many independent triggers, one shared swap
target" — the `replace` strategy aborts whatever request is currently
in-flight for that sync group the instant a new one is dispatched, so only
the most-recently-fired request's response is ever allowed to complete and
swap; the older one never gets a chance to land at all, whether its response
would have arrived early or late).

**Files/lines touched** (17 occurrences across 6 files — every
`hx-target="#basket"` in the `web/ui/` tree, confirmed by grep before and
after):

- `web/ui/pages/index.html` — kiosk "New Sale" reset button, Hold Sale form,
  scan-row form, per-payment-method pay buttons (both the plugin-driven
  `range` branch and the `{{ else }}` cash/card fallback), New Customer
  reset button in the payment overlay footer (7 occurrences)
- `web/ui/partials/basket.html` — Dine-in/Takeaway order-type toggle
  buttons, per-line qty input, per-line discount input, remove-line (✕)
  button (5 occurrences)
- `web/ui/partials/buttons.html` — product-tile quick-add button (no
  modifiers) (1 occurrence; the modifier-picker-opening tile variant targets
  `#modifier-modal`, not `#basket`, so it's correctly untouched)
- `web/ui/partials/modifier_picker.html` — scan-with-modifiers form (1)
- `web/ui/partials/suggestions.html` — "Customers also buy" chip button (1)
- `web/ui/partials/table_picker.html` — clear-table button, per-table
  assign button (2)

Verified no other `#basket` selector variants (single-quoted, etc.) were
missed, and that `#selforder-cart` (the separate customer-facing self-order
flow's own shared swap target) is out of scope for this ticket — the report
is specifically about the operator-facing POS basket.

### A real mistake caught during this fix, worth recording

The first edit pass to `basket.html`'s Takeaway button accidentally dropped
the closing `>` of the button's opening tag while inserting the new
attribute (a plain string-replace slip, not a logic error) — the tag stayed
open, `html/template` failed to execute the `basket` fragment, and
`internal/pages/basket_page.go`'s handler (`_ = basketView.Render(w, b)`)
silently swallowed the render error, so `/ui/basket` started returning **200
OK with an empty body**. This broke every e2e spec that loads the sale
screen, not just the new one — caught by `gofmt`/`go build` staying green
(a malformed HTML attribute isn't a Go syntax error) but the *existing*
`sale.spec.ts` failing identically to the new race spec when run against the
broken template. Fixed by restoring the `>`; re-verified via `git diff`
against `origin/main` that every other file's edits keep every tag closed.
Flagging this here because it's exactly the kind of self-inflicted false
"reproduction" a rushed TDD pass can produce — the first "pre-fix test
fails, post-fix test passes" result was actually "broken template masks
whether the fix works at all," not the race being proven.

## Verification

### 1. Proving the race (TDD)

Wrote `e2e/tests/basket-hx-sync-race-1337.spec.ts`. A genuine network race
(two browser-timed requests actually crossing in flight) is not reliably
reproducible under Playwright's normal await-each-step idiom, so the test
instead makes the interleaving **deterministic** via route interception:

- Two `page.route()` handlers (order-type, and the specific Pepsi scan) each
  perform the **real** request immediately via `route.fetch()` — the
  server-side mutation commits right away, exactly like a real request —
  but withhold delivering the response to the browser until the test
  explicitly releases it.
- Both triggers are fired close together, unawaited, while the same
  `#basket` node (from an earlier, already-completed Coca-Cola scan) is
  still current, so htmx resolves both `hx-target`s to that same node —
  mirroring an operator tapping Takeaway then quickly scanning another item.
- The test releases the **older-fired** (order-type) response first, waits
  for its swap to settle, then releases the **newer-fired** (scan) response
  second — forcing exactly the ordering the field report's symptom implies:
  an earlier action's response wins the delivery race.

**Confirmed both directions empirically, not assumed:**

- Against pre-fix code (`git stash` of the six template files, keeping only
  the new test): the test **fails**. The order-type toggle visibly switches
  to "✓ Takeaway" (its swap succeeded — first responder, original node still
  attached), but **Pepsi never appears anywhere in `#basket`** — the exact
  "item silently disappeared" symptom, reproduced on demand rather than
  hoped for.
- Against post-fix code (`git stash pop`): the test **passes** — both the
  order-type switch and Pepsi are present in the final DOM. (Mechanism:
  `hx-sync="#basket:replace"` on the scan trigger aborts the order-type
  request the instant the scan fires, so the order-type response never gets
  to land and detach the shared node in the first place; the scan response
  — the only one left in flight — completes normally against the original,
  still-attached node.)

This is a real, reproduced-both-ways proof, not a test that "happens to
pass either way."

### 2. Full existing e2e suite

`npx playwright test --project=default` (234 specs). `hx-sync` only changes
behavior for *overlapping* requests to the same sync group; the vast
majority of existing specs drive one action at a time and await each step,
so they were expected to be unaffected. Ran it **twice** against the
post-fix code to separate a real regression from pre-existing suite
flakiness:

- Run 1: 228 passed, 5 failed, 1 flaky.
- Run 2 (identical code, no changes in between): **234 passed, 0 failed.**

Also ran the full suite once against pre-fix `origin/main` (via `git stash`
of the six template files, new spec excluded) for a baseline: 231 passed, 2
failed — a **different pair** of specs than run 1's five. Different specs
failing across three runs of otherwise-identical code is the signature of
this suite's known shared-server-state ordering flakiness (every spec in
the `default` project runs against **one** till server process — see the
isolation note below and `sale-screen-213.spec.ts`'s own comment on this),
not something `hx-sync` introduced: run 2 proves the fixed code passes the
entire suite cleanly, and the failure sets not matching between the pre-fix
and post-fix runs rules out a stable regression.

Isolation note: since specs share one server, the new spec's `afterEach`
explicitly calls `POST /api/pos/reset` (the same documented pattern
`sale-screen-213.spec.ts` already uses) to avoid leaking basket state into
whichever spec runs next — confirmed necessary during development: an
earlier version of this spec without the reset cascaded a leftover £4.75
basket total into `sale.spec.ts`'s own assertion in the same run.

### 3. Standard gate

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — two failures,
  both **pre-existing and unrelated to this change**: `internal/alerts`'s
  `TestStart_RunsDigestLoopBody` (a fast-forwarded-timer digest-loop test,
  fails consistently under this sandbox's system load — reproduced 3/3 in
  isolation) and `internal/server`'s
  `TestListenWithFallback_WildcardHostFallsBackToLoopback` (asserts a
  wildcard host falls back to IPv4 loopback; this sandbox's dual-stack
  network config instead resolves to `[::]`, an IPv6-vs-macOS-network-stack
  difference from the `ubuntu-latest` CI runner this repo's CI actually
  uses). Neither package is touched by this change — the entire diff is six
  `.html` template files — so both failures are structurally impossible to
  be caused by it; confirmed by reproducing them in isolation with `-run`
  and `-count=3`. Every other package passed, including `internal/pages`
  (which owns `/api/pos/*` and `/ui/basket`) and `internal/pos`.
- CI's guard scripts most relevant to this change — `guard-htmx-loaded.sh`,
  `guard-i18n.sh`, `guard-autofill-suppression.sh`, `guard-osk-loaded.sh` —
  all pass locally. Full `.github/workflows/ci.yml` guard list: see PR CI
  (authoritative; this diff shouldn't trip any of the others either, since
  none touch server-side data access, migrations, kiosk-engine isolation,
  plugin-menu reads, compliance wording, or the Android/webkit guards).

### 4. Concurrent-PR merge conflict, mid-review

While this PR sat waiting on CI, PR #667 (ut-docs#1314, basket item-name
column width — a genuinely different, independently-developed fix touching
`web/public/app.css`) merged to `main` first. Both PRs touch the
`web/ui/**`/`web/public/**` surface `guard-docs-shots.sh` hashes, so both
regenerated `web/help/img/manifest.json` and every screenshot independently
— a real conflict, not a mistake by either side. Resolved by merging
`origin/main` into this branch, taking `main`'s screenshots for the merge
itself, then re-running `make docs-shots` fresh against the fully-merged
tree so the manifest and every screenshot reflect **both** changes
combined. Re-verified after the merge: `gofmt`/`go build` clean,
`internal/pages`/`internal/pos`/`internal/ui` Go tests still green, and the
full e2e suite (238 specs, one more than the pre-merge 234 — #1314 added
its own spec) **238/238 passed** in a single run — no interaction issue
between the two changes (#1314's basket-row layout change and this PR's
`hx-sync` additions touch disjoint concerns: row CSS vs. request
sequencing).

## Scope notes

- `#selforder-cart` (the separate customer-facing self-order kiosk flow,
  `web/ui/partials/self_order_cart.html` and friends) shares the identical
  "many triggers, one swap target, no `hx-sync`" shape and is very likely
  exposed to the same class of bug — but it's a different target selector,
  a different user (the customer, not the operator), and out of scope for
  this p1 ticket, which is specifically about the operator POS basket. Worth
  a follow-up card.
