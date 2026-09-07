# Refund POST: per-line qty cap must net a line's own return history (ut-docs#1583)

**Card:** universaltill/ut-docs#1583 — "Refund POST validates requested qty
against the per-key pool only, never against the specific original line's
own sold qty." Found by independent review of ut-docs#1560 (PR #785).
**Repo/branch:** universal-till, `fix/1583-refund-per-line-qty-cap`
**Complexity:** escalated `easy` → `medium` mid-cycle (scrum-master's
"escalate instead of grinding" rule) after round 1's review found the
originally-scoped one-line fix was still exploitable — noted on the issue
before continuing. Dev at Sonnet throughout; Review at Sonnet (round 1,
per the `easy` label still in force at that point) then Opus (rounds 2-3,
per `medium`).

## What shipped

`POST /api/refund`'s handler validated a requested `qty_<i>` against the
shared per-refund-line-key pool (`pool[key]`) only, never against that
specific original sale line's own sold quantity (`l.Qty`) — unlike the
page's own display logic (`refundableLines`), which already capped at
`min(l.Qty, pool[key])`. A hand-crafted POST bypassing the rendered page's
per-line max could request more against one specific line than it ever
sold, as long as the request stayed within the combined pool shared by
sibling lines under the same item/variant/price/mode key. Since ut-docs#1560,
every return line persists `refund_of_line_id` pointing at the specific
original line it came from, so this gap could leave that ledger internally
inconsistent (`returnedQtyByLine[lineID] > originalLine.Qty`).

Final fix, after three review rounds each finding a real, distinct gap in
the previous round's fix:

1. **POST cap nets the line's own return history, not just its original
   sold qty.** `lineRemaining := l.Qty - returnedQtyByLine[l.ID]` (clamped
   at 0), then `remaining := min(pool[key], lineRemaining)`.
   `returnedQtyByLine` (`repo.ReturnedQuantitiesByOriginalLine`) was
   already loaded in the same handler for the non-uniform discount branch
   — just not wired into the qty cap.
2. **The GET display uses the identical basis**, so it can never offer a
   quantity the POST then refuses. `refundableLines` gained a third
   parameter (`returnedByLine map[string]float64`, keyed by original
   `sale_lines.id`) and applies the same net-then-pool-cap computation the
   POST handler uses; the GET handler now also calls
   `ReturnedQuantitiesByOriginalLine` and threads it through.
3. **Four regression tests**, each pinning one round's finding:
   - `TestPostRefund_LineQtyCappedAtItsOwnSoldQty` (round 1: single-request
     over-request against one line, reusing the #1560 fixture — line A qty
     3 discount 10, line B qty 1 discount 0, sharing a key).
   - `TestPostRefund_SequentialDoubleDipAgainstSameOriginalLine` (round 2:
     the same line refunded twice across two sequential requests, masked
     by a sibling's untouched pool room).
   - `TestRefundableLines_EarlierLinePreRefundedStillOffersLaterLineInFull`
     (round 3, pure-function) and
     `TestRefundPage_EarlierLinePreRefundedStillOffersLaterLineViaPOST`
     (round 3, driven GET+POST round trip through the real mux): two split
     lines share a key, the EARLIER line (by index) is already fully
     refunded — the later, untouched sibling must still be offered its
     own full remaining quantity, and POSTing exactly what the page
     offered must succeed. This is the shape the original
     `TestRefundableLines_SplitLinesShareTheSamePool` fixture happened not
     to exercise (its pre-refunded amount is pool-level only, and its two
     lines are otherwise symmetric), which is exactly why the round-2 fix
     shipped a display/POST disagreement undetected.

## Independent review — three rounds, escalating in scope, each closing a real gap

**Round 1 (fresh-context Sonnet, `complexity:easy`'s prescribed reviewer).**
Verified the TDD claim by reverting the fix and reproducing the exact
"got 200, want 409" failure, ran the full gate (clean), then found a real
follow-on gap: capping at `l.Qty` alone doesn't net a return already
recorded against that specific line in an earlier request, so the same
line could be double-dipped across two sequential POSTs. Confirmed with a
driven probe against the shipped fix, not just reasoned about.

**Round 2 (Opus, escalated per the complexity bump).** Re-verified round
1's fix and the round-2 TDD claim (revert → exact predicted failure →
restore → pass). Then found the round-2 fix itself created a new,
*more severe* regression: netting the POST cap without netting the
display made the page offer a quantity the POST then refused — a
legitimate refund became impossible from the till entirely (the template
hides the input for a line whose computed `Remaining` is 0, so there was
no way to submit the correct allocation from the UI at all). Proved this
was a genuine regression against round 1 (not pre-existing) with a driven
repro, and experimentally validated the remedy (thread the same netting
into `refundableLines`) before handing it back.

**Round 3 (Opus, scoped to the round-2 remedy).** Re-verified the TDD
claims for both new round-3 tests (revert → fail → restore → pass),
proved algebraically that the display's offer is always ≤ what the POST
will accept (so they can no longer disagree in the unsafe direction), ran
the full gate (`go build`/`vet`/`test ./internal/pages/...` — the whole
package, not just the new tests — `gofmt`, `guard-data-access.sh`,
`guard-i18n.sh`, `guard-page-http-error.sh`, `guard-help-topics.sh`,
`golangci-lint`), hunted for a fourth gap (none found — checked the
original `TestRefundableLines_SplitLinesShareTheSamePool` still proves
what it always did, checked `sync_sales.go`'s separate, deliberately
non-blocking over-guard re-check for the same gap and confirmed it's out
of scope per ADR-0011, and empirically verified the display path's float
handling round-trips exactly through the template with no epsilon needed).
Verdict: safe to merge, with one non-blocking test-hardening nit.

**Nit, fixed.** Round 3 flagged that
`TestRefundPage_EarlierLinePreRefundedStillOffersLaterLineViaPOST`'s two
independent, unanchored `strings.Contains` checks didn't actually
discriminate against a hypothetical bug that left line 0's own `max="2"`
in the page while under-offering line 1 — the test only failed on the
`name="qty_1"` half. Tightened to a single anchored regex
(`name="qty_1" value="2"\s+min="0" max="2"`) matching the real rendered
attribute order/whitespace (verified against the actual template output,
not guessed), re-confirmed it still fails correctly against the round-3
revert and passes with the fix.

## Verified beyond automated tests

- Full `go test ./internal/pages/...` green after every round (not just
  the new tests) — 64-71s per run across three re-verifications.
- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` on all three
  changed files: clean throughout.
- `golangci-lint run ./internal/pages/...`: 0 issues.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-page-http-error.sh`,
  `guard-help-topics.sh`: all pass (no SQL added outside `internal/data`,
  no new user-facing string, the one new error branch uses the same
  `common.LogAndLocalizedError` pattern as its siblings, no route/topic
  change).
- No UI/route/template structural change beyond the existing `refund.html`
  input's pre-filled value now agreeing with the server — no e2e run
  needed (backend + existing HTMX-rendered form, no new page or route).
- No real client/shop name or secret-shaped literal in any new test data
  (`Widget`, synthetic item/receipt/sale IDs, `GBP`/`cash`).
- Confirmed no other call site needed the same fix:
  `internal/pages/sync_sales.go`'s `RefundLineKey` use is a deliberately
  non-blocking, log-only over-guard re-check for an already-committed
  satellite journal entry ("the money already moved at the till... flagged
  for the manager", ADR-0011) — a different, already-accepted design, not
  a regression or a duplicate gap.

## Safe to merge

Yes. Three review rounds, each earned by the previous round's genuine
blocker-class (data-integrity / functional-regression) finding, per
scrum-master's "second round has to be earned" rule — none was a rubber
stamp or a re-review of unrelated ground. Final state: POST validation and
the page's own display now share one identical net-then-pool-cap basis,
so they can never disagree, closing both the originally-reported gap and
the two follow-on gaps the review process itself surfaced.
