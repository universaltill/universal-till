# Code review: contextual "?" route coverage + `helpLink` (ut-docs#326)

**Card:** ut-docs#326 — closes the page-route-coverage gap tracked as
ut-docs#365. Escalated `complexity:medium` → `complexity:hard` mid-cycle
(comment on the issue) after the design surfaced a real runtime/CI mismatch,
not mechanical wiring. Dev at Fable, independent review at Opus (deliberately
not Fable), per the hard-tier model-routing rule.

## What shipped

1. **Pattern-aware route resolution** (`internal/manual/manual.go`) —
   `Library.TopicForRoute` kept its exact-match fast path and gained a
   segment-wise fallback (`RouteMatches`) so a topic declaring
   `/invoice/{display_no}` actually resolves the "?" for a live request to
   `/invoice/12345`. The old exact-string-only lookup could never have fired
   for that case.
2. **Page-route coverage closed** — `/backoffice`→alerts,
   `/invoice/{display_no}`→invoices, `/journal/{receipt}`→reports,
   `/refund/{receipt}`→sell, `/plugins/{id}/settings`→plugins, plus three new
   minimal stub topics (`menu`, `translations`; `self-order` content is
   intentionally thin, ut-docs#338 owns the real version) — all four locales.
3. **`helpLink "topic-id"` template helper** (`internal/httpx/httpx.go`) —
   for a section INSIDE an already-covered page (settings.html's
   claim/updates/payments/backups/printing cards), which can't take its own
   `routes:` claim without tripping the duplicate-route guard.
4. **4th CI guard check** (`scripts/ci/checkhelptopics/routecoverage.go`) —
   AST-scans `internal/pages/**` for `mux.HandleFunc`/`mux.Handle`
   registrations, denylists non-page namespaces by prefix (each with a
   documented reason), and requires everything else to resolve via the same
   `RouteMatches`/`RouteCovered` logic the runtime uses — one algorithm, not
   two that can drift apart.
5. **Widened `e2e/tests/manual.spec.ts`** — new coverage for `/backoffice`,
   `/menu`, a full cash-sale → receipt-detail walk proving the parameterized
   matcher works end-to-end (not just in a Go unit test), and a settings
   spec asserting all five section hints render and target the right topic.

## Independent review (Opus, fresh context, did not write the code)

Verified the diff builds, vets, and passes the full race suite (only the
known pre-existing `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
environmental failure under uid 0, confirmed also present on unmodified
`main`), all three guards including the new 4th check, and `gofmt -l` clean.
Ran a real negative test on the new guard (temporarily registered an
undocumented route, confirmed the guard fails loudly and names it, removed
it, confirmed green again — the check is not vacuous). Independently
re-verified the TDD claim on the pattern-matching fix by reverting the logic
twice (once each for `TopicForRoute`/`RouteMatches` and again including
`RouteCovered`) and confirming the new tests fail without the fix, pass with
it restored.

**Findings, all addressed in this diff (none deferred as "won't fix"):**

1. **Directional matching gap (blocker-adjacent — this is why the card was
   escalated to hard in the first place)**: the original `RouteMatches` was
   symmetric — a `{param}` on *either* side counted as a match. That let
   `RouteCovered` (used by the CI guard) report a hypothetical registered
   `GET /plugins/{id}` as "covered" by the existing literal `/plugins/store`,
   while `TopicForRoute` (used at runtime) would still never resolve that
   request, because a concrete registered pattern with a `{param}` segment
   is not the same thing as a concrete request path. No live route hits this
   today, but it was one new page away from shipping a guard that's green
   while the feature is silently broken — precisely the failure class this
   card exists to close. **Fixed**: `RouteMatches` is now directional
   (`pattern` first, `path`/registered-pattern second) — a `{param}` only
   counts as generic on the declared/pattern side. Regression tests added
   in both `TestRouteMatches` and `TestRouteCovered`, using a fixture that
   isolates the exact shape (a literal-only declared route with no sibling
   pattern of the same segment shape) — confirmed these fail on the old
   symmetric logic and pass on the fix by reverting and re-running locally.
2. **`/self-order`, `/self-order/shop` claimed a "?" that can never
   appear** — both pages render via `httpx.RenderPartial` (no base
   layout/nav), so there is no `.help-hint` on screen to link from. The
   original `routes:` claim would have made the CI guard green while
   documenting a link that doesn't exist. **Fixed**: removed the `routes:`
   line from `self-order.md` (all 4 locales — the topic itself stays,
   reachable via manual search, same as `quickstart`'s routeless
   convention), added `/self-order` to the guard's denylist with its reason,
   and added one sentence to the topic prose (4 locales) explaining why it
   has no on-screen link. Updated `TestRouteRegistryResolvesKnownPages`
   accordingly and added denylist test cases.
3. **`/backoffice`→alerts: the topic never mentioned the page** — alerts.md's
   prose sent the reader to Inventory/Reports without ever naming the "Back
   office" screen it now claims. **Fixed**: added a lead step describing the
   Back office glance screen, 4 locales.
4. **`/refund/{receipt}`→sell: right topic, thin prose** — agreed sell is
   the correct topic (a till action, not an analysis surface; reports would
   be circular since the refund screen links back to `/journal/{receipt}`),
   but the existing text only said "refunds are under the sale history,"
   nothing about the refund screen itself (per-line remaining quantity,
   partial refunds, choosing cash vs. the original payment method).
   **Fixed**: added a step describing this, 4 locales.
5. **No mechanical guard on `helpLink` ids outside settings.html** — a typo
   in a *future* `helpLink "..."` call elsewhere would silently degrade to
   `/help` with nothing failing. Accepted as a follow-up, not folded into
   this diff (a new card, filed as ut-docs#381) — `helpLink`'s current
   degrade-to-`/help` behavior is itself deliberate for an unknown id (this
   card's own httpx test covers that it doesn't panic), and generalizing the
   check belongs in `guard-i18n.sh`'s existing template-scanning machinery,
   a different guard than the one this card owns.
6. **`{name...}`/`{$}` Go 1.22 wildcard forms treated as ordinary `{name}`
   segments** — no live effect (the one route using `{file...}` is
   denylisted, not declared by any topic), documented as a known limitation
   in `RouteMatches`'s doc comment rather than special-cased now.

Findings #1–#4 required behavior/content changes and were applied by the
orchestrator (not the reviewer) after the review, then the full gate
(build/vet/test -race/all three-now-four guards/gofmt) was re-run clean.
Finding #2 required updating one existing test's expectations
(`TestRouteRegistryResolvesKnownPages` no longer expects `/self-order` to
resolve). The `RouteCovered` regression test added for #1 was itself
re-verified against a fixture that isolates the actual bug shape after an
initial version of that test was found to be vacuous against the synthetic
test fixture (the fixture didn't originally contain a literal-only route
with no sibling pattern of the same shape) — corrected and re-confirmed to
fail-then-pass across the revert.

## Verified beyond automated tests

- Full `go test ./... -race`, `go build ./...`, `go vet ./...`, and all four
  `scripts/ci/*.sh` guards, run twice (once before the review's fixes, once
  after) — clean both times bar the one known pre-existing failure.
- `gofmt -l` on every changed/new `.go` file — clean.
- The Dev subagent's Playwright run (65/65, full default project against a
  real till + Chromium) is reported in its own transcript; not re-run in
  this sandbox pass (proxy blocks the Playwright CDN download — see the
  Dev subagent's own note on its local workaround). The manual e2e coverage
  itself was read and judged sound by both the Dev and independent review.
- TDD claim on the core pattern-matching change re-verified twice by
  reverting the logic and confirming the exact new tests fail, then
  restoring and confirming green — not taken on the implementer's word.

## Deferred (new backlog card, not folded into this diff)

- **ut-docs#381** — generalize `helpLink "id"` typo detection into
  `guard-i18n.sh`'s existing template-scanning pass (compare every
  `helpLink "..."` call site against `manual.IDs()`), so a typo'd topic id
  anywhere (not just settings.html, which has an explicit test today) fails
  CI loudly instead of silently degrading to `/help`.

## Safe-to-merge verdict

**Yes.** No open findings block the merge; #1–#4 were fixed and re-verified,
#5 is filed as a follow-up, #6 is a documented, currently-inert limitation.
