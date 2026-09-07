# Refund: running per-key line-discount clamp (ut-docs#1531)

**Card:** universaltill/ut-docs#1531 — "Refund: per-request line-discount
proration floors, letting sequential partial refunds over-refund the
subtotal by a minor unit."
**Repo/branch:** universal-till, `fix/1531-line-discount-refund-clamp`
**Complexity:** medium (Dev at Sonnet, Review at Opus — two independent
rounds, both found blocker-class issues; see below)

## What shipped

`internal/pages/refund_page.go`'s `POST /api/refund` handler used to
prorate a partially-refunded line's discount **independently on every
request**: `share := qty / l.Qty; lineDiscount := int64(float64(l.LineDiscount)
* share)`. Flooring every time meant several sequential partial refunds of
the same line could cumulatively give back *less* discount than owed —
i.e. *more* net than owed — over-refunding the subtotal by a minor unit
(driven repro: 3 @ 100, discount 10, refunded 1 unit at a time: 291
returned vs. the true 290).

Fix, in three layers, built up across two independent review rounds:

1. **Core clamp.** New repo method `ReturnedLineDiscounts` (mirrors the
   existing `ReturnedQuantities`/`RefundedServiceChargeTotal`) sums
   `line_discount` already given back per `RefundLineKey`. The handler now
   targets the *cumulative* discount that should have been given back by
   the end of each request — `floor(keyDiscount * cumulativeQty / keyQty)`,
   exact once the full quantity is refunded — and hands back only the
   remainder over what prior completed returns already paid. Same
   telescoping-clamp shape as #1215's `RefundedServiceChargeTotal` guard,
   applied per line-key instead of once for the whole sale.
2. **`keyUniform` fallback (review round 1, finding F1 — BLOCKER).**
   Aggregating discount by key is only exact when every original line
   sharing that key applies the same discount rate. Two lines of the same
   item/price/mode with *different* `LineDiscount` (e.g. one manually
   discounted, its scanned-twice sibling not) would otherwise have one
   line's discount misattributed onto the other's refund — driven repro
   showed a 25-minor-unit cross-attribution. A key whose lines don't share
   a uniform rate now falls back to the pre-#1531 independent per-line
   floor (today's existing, previously-accepted behavior — not a
   regression for that shape).
3. **Gross cap + epsilon snap (review round 1, findings F3/F4; round-2
   finding B1 fixed the cap's own rounding basis).** The computed
   `lineDiscount` is capped at the request's own gross (using
   `pos.AmountForQuantity(...).Minor()` — the SAME rounding basis
   `refundNet` a few lines below and `pos.CompleteSale`'s own line-base
   check already use, not a plain truncating `int64(float64(...))`, which
   review round 2 caught paying out money never collected on a fractional
   comped line and silently disengaging #1215's B3 zero-net-weight
   service-charge fallback). `cumulativeQty` (a float) snaps to the key's
   exact total once within the same `1e-9` epsilon the handler's own
   remaining-quantity check already uses, so a fractional original `Qty`
   landing a hair short of its true total via float arithmetic doesn't
   revive the exact bug this card exists to eliminate.

## Independent review — two rounds, both at Opus, both found a blocker

**Round 1** (fresh-context Opus subagent, isolated worktree): found F1
(BLOCKER — see above), F3 (no upper bound on `lineDiscount`, could go
negative against legacy pre-fix return rows), F4 (float-precision boundary
miss), plus nits N1–N4. Independently re-verified the TDD claim by
reverting the fix and confirming the new regression test failed with the
exact over-refund, then confirming it passed restored.

All of F1/F3/F4 were fixed. Fixing F1 required adding the `keyUniform`
fallback and two new tests; F3/F4 were folded into the same clamp block.

**Round 2** (fresh-context Opus subagent, isolated worktree, scoped to the
round-1 fixes): re-verified F1's fix genuinely holds (reverted just the
`keyUniform` branch, confirmed the new test fails with the same
cross-attribution, restored, confirmed it passes) and, critically, found
that F4's own regression test was a **false-pass** (B2) — its original
fixture (0.3 sliced as three 0.1s) happened to land on the *safe* side of
the float boundary, so the test passed identically with the epsilon snap
disabled. It also found a **new blocker (B1)**: the F3 gross cap truncated
where the money layer (`pos.AmountForQuantity` → `money.MulQty` →
`math.Round`) rounds, so on a fractional quantity whose gross has a `.5+`
fraction the two could disagree by one minor unit — paying out money never
collected on a comped line, and (because that shifted the request's own
`refundNetWeight` off exactly-zero) silently disengaging #1215 finding
B3's zero-net-weight service-charge fallback with no test to catch it. The
reviewer supplied and pre-tested the exact one-line fix
(`pos.AmountForQuantity(money.FromMinor(l.UnitPrice), qty).Minor()`,
matching `pos/sales.go`'s own `lineBase` check), confirmed it against their
own fuzz sweep (400 randomized cases: `overshoot=0 uniform-shortfall=0`)
before reporting back.

Both B1 and B2 are fixed here: B1 via the exact one-line change the
reviewer verified themselves; B2 by replacing the F4 test's fixture with
the actually-dangerous boundary (0.9 units sliced as three 0.3s — confirmed
by the round-2 reviewer, driven against the real handler, to fail without
the snap and pass with it).

A third full independent-agent round was **not** re-run for B1/B2: the
round-2 reviewer had already applied and tested the exact B1 fix in their
own worktree (their own fuzz sweep against the candidate came back clean)
before reporting it, and B2 is a test-fixture-only change confirmed the
same way. This session's own full gate (see below) was re-run clean after
applying both. Judged sufficient corroboration rather than spending a
third full review round on an already-validated one-line fix.

Nits addressed inline rather than deferred (all cheap, all money/robustness
adjacent):
- **N2** — the non-uniform fallback branch's `share := qty / l.Qty` is now
  guarded against `l.Qty <= 0` (falls through with `lineDiscount = 0`
  instead of dividing by zero / relying on downstream rejection).
- **N3** — the `lineDiscount < 0 → 0` clamp is hoisted to cover BOTH
  branches (uniform and fallback) in one place, matching what the
  surrounding comment already claimed.
- **N4** — added `TestReturnedLineDiscounts`, a direct `internal/data`
  test for the new repo method (mirrors the existing
  `TestReturnChain_ReceiptsAndReturnedQuantities`), including a
  non-`completed` return correctly excluded.
- **N5** — corrected the `ReturnedLineDiscounts` doc comment's
  "same line" wording to "same key," and removed language that overclaimed
  exactness before the `keyUniform`/gross-cap guards existed.

**N1 — deliberately deferred, tracked separately.** A key whose sibling
lines carry genuinely different discount rates still uses the pre-#1531
independent-floor fallback, which does NOT fully close the card's own
"never exceeds" invariant for that narrower shape (a round-2 fuzz sweep
found ~17% of randomly-generated non-uniform-key cases still under-refund
the discount slightly). This is correctly documented in the code comment
and is strictly not a regression — it's the same behavior that shipped
before this card, for that one shape — but the card should not be read as
"the bug is gone for every possible line/discount configuration." Filed as
ut-docs#1560 (new Backlog card) rather than re-scoped into this PR: fixing
it properly needs `ReturnedLineDiscounts` (or a sibling method) to track
discount **per original line**, not per key, which the current schema
can't disambiguate from stored return rows alone (a return only records
its `RefundLineKey`, not which specific original line it was refunded
against) — a real, separate design question, not a one-line fix.

**F2 — also deliberately deferred, also filed separately (ut-docs#1561).**
A heavily-discounted line (average net-per-unit near zero, e.g. a
near-100%-discounted line) can compute a marginal net of exactly 0 for a
partial request, which `pos.netPayments` rejects (`amount must be > 0`),
blocking that specific partial refund even though 0 is the mathematically
correct marginal amount. This is a collision between this card's proration
fix and the payment-validation layer's own invariant, not a proration bug
— explicitly out of scope for this card (see the original issue's own
non-goals). The round-2 review confirmed F4's fix makes this collision
*more* precise (hit slightly more often) rather than worse, since the
marginal net used to be silently wrong (over-refunding by masking) instead
of being exactly, correctly zero.

## Verified beyond automated tests

- `gofmt -l .` clean.
- `go build ./...` clean.
- `go vet ./...` clean.
- `go test ./...` — full suite green (all packages).
- `go test ./internal/pages/... ./internal/data/... -race` — green (the
  one apparent failure mid-cycle was the default 10-minute `go test`
  timeout on the full, large `internal/pages` package under `-race`
  instrumentation, not a real race or test failure — confirmed by
  re-running with `-timeout 20m`, which passed clean in 775s; this is a
  pre-existing package characteristic unrelated to this diff).
- All 18 CI-blocking guard scripts in `.github/workflows/ci.yml`'s `build`
  job pass, including `guard-data-access.sh` (the new repo method's SQL
  lives only in `internal/data`, per this repo's non-negotiable).
- Both independent review rounds actually reverted the fix and re-ran the
  regression tests to confirm they fail pre-fix with the exact claimed
  error, then confirmed pass post-fix — not taken on the implementer's
  word.
- Round 2 additionally ran a 400-case randomized sweep driving the real
  HTTP handler (mixed uniform/non-uniform keys, fractional quantities,
  random split sequences) asserting the card's own invariant
  (cumulative persisted `line_discount` vs. the key's true total):
  `overshoot=0` on every case, `uniform-shortfall=0` (exact recovery)
  on every uniform-rate key.

## Safe-to-merge verdict

**Safe to merge.** Both review rounds' blocker-class findings (F1, B1) are
fixed and re-verified; the should-fix findings (F3, F4→B2) are fixed and,
for B2, the fix itself was corrected to actually pin the invariant it
claims to. Remaining nits (N2, N3, N4, N5) addressed inline. Two items are
deliberately out of scope and tracked as new Backlog cards rather than
silently dropped: N1 (non-uniform-key exactness — ut-docs#1560) and F2
(zero-marginal-net partial refunds hitting the payment layer's own
invariant — ut-docs#1561).
