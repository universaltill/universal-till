# Held-order table-claim periodic re-affirm

Card: ut-docs#1724 · Lane: `lane:cloud-54` · Complexity: medium (Sonnet build, Opus review)

## What shipped

ut-docs#1724: a held (parked) order's cross-till table occupancy had no
periodic re-affirm while a till kept running — only Init's boot re-claim
step (ut-docs#1704) ever re-established it, so a till that lost its
PRIMARY-side `table_claims` row for a held order's table (for any reason —
most concretely, another till taking it over during a primary-reachability
outage and later releasing or itself going stale) stayed wrong until that
till's next restart, not just for the outage window.

This adds the periodic sibling:

1. **`internal/pages/tables_claim_proxy.go`** — new shared helper
   `reaffirmHeldOrderTableClaims(ctx, d, posRepo, held, logPrefix,
   logRefusalAsError)`: write-throughs `claimTableWriteThrough` (the
   existing, tested primitive) for every held order with a table. Shared by
   both Init's boot re-claim step (refactored to call it) and the new
   periodic tick, so they can't drift apart.
2. **`internal/pages/sync_admin.go`** — new `StartHeldOrderClaimReaffirm`,
   reusing the existing `runSyncLoop` primitive (same 30s cadence as
   `StartSyncPull`/`StartSyncPush`) to drive `heldOrderClaimReaffirmTick`,
   which lists held sales and calls the shared helper (short-circuits with
   no held orders — no wasted DB/network cost in the common case).
3. **`internal/pages/init.go`** — wires `StartHeldOrderClaimReaffirm` right
   after `StartSyncPull`.
4. **`internal/pages/held_order_claim_reaffirm_test.go`** (new) +
   `sync_admin_test.go` (one new test) — see Verification below.

## Verification

TDD, genuinely: `heldOrderClaimReaffirmTick` was temporarily forced to an
early `return` (before the fix, and again after the review's fixes below),
each time confirming `TestHeldOrderClaimReaffirmTick_RecoversADeletedClaimWithinOneTick`
fails with a real assertion message (not a panic/compile error: `"T1 must
read occupied on the primary again ... free=true err=<nil>"`), then
restoring and confirming the full targeted test list passes again.

Full gate green: `go build ./...`, `gofmt -l .`, `go vet ./...`, full-repo
`go test ./... -count=1` (no sibling-package regressions), full-repo
`golangci-lint run ./...` (0 issues), `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-htmx-loaded.sh`.

Backend-only change — no page/handler/template/locale key touched, so
`reference/ux-guidelines.md`'s checklist and a driven browser run are
correctly out of scope (confirmed by the independent review too, not just
assumed).

## Independent review

Opus subagent (`complexity:medium` routing), isolated worktree, ran the
full gate itself (build/vet/gofmt/golangci-lint/full `go test ./...`
including `-race` on the new and adjacent loop paths, plus the relevant CI
guards) and independently re-verified the TDD claim by reverting the fix
and confirming the exact failure.

**Verdict: not safe to merge as first submitted — four real findings, all
fixed before commit:**

- **F1 (major, real bug — a documentation defect that actively misleads).**
  The new function's doc comment was pasted directly above
  `claimTableWriteThrough` with no blank line, so godoc silently attributed
  the pre-existing ~30-line design-rationale comment (the ut-docs#1704 /
  2026-09-07-review reasoning about the `""` till id) to the *new* function,
  leaving `claimTableWriteThrough` itself with zero documentation. **Fixed**:
  moved `reaffirmHeldOrderTableClaims` and its own comment below
  `claimTableWriteThrough`'s closing brace, restoring the original comment
  exactly where it was.
- **F2 (major, real — the shipped code's own reasoning was wrong).** Every
  doc comment (the helper, `StartHeldOrderClaimReaffirm`) and the original
  test's own rationale/failure message claimed the fix works by "refreshing
  `claimed_at` so the row never goes stale under `ClaimTableForTill`'s TTL
  reconciliation." The reviewer proved this false with a throwaway test:
  `ClaimTableForTill`'s staleness predicate (`internal/data/tables_repo.go`)
  reads `tills.last_seen_at` only — `claimed_at` is write-only bookkeeping,
  read back solely for the floor plan's "occupied since" display
  (`tables_repo.go:704`). Concretely: an ancient `claimed_at` with a fresh
  `last_seen_at` is refused; a brand-new `claimed_at` with a stale
  `last_seen_at` succeeds. The fix is still real and worth shipping — it
  just does something different from what it claimed: **recovery** of a
  claim row that went missing (re-created via an ordinary `INSERT` once
  nothing conflicting remains), not **prevention** of staleness via
  `claimed_at`. It also cannot, and must not, forcibly evict a different,
  currently-live till's legitimate claim — `ClaimTableForTill` only ever
  evicts a row that reads stale by `last_seen_at`. **Fixed**: rewrote every
  doc comment (`reaffirmHeldOrderTableClaims`, `StartHeldOrderClaimReaffirm`)
  and the test file's rationale/assertions to describe the real mechanism.
- **F3 (major, real — the original test proved the wrong property).** The
  first test version asserted only that a second till's takeover attempt was
  refused after the tick — satisfied by `syncTill` touching
  `tills.last_seen_at` on *any* bearer-authed round trip, including the
  pre-existing `StartSyncPull` tick this change didn't add. It never
  isolated this change's actual contribution. **Fixed**: replaced with
  `TestHeldOrderClaimReaffirmTick_RecoversADeletedClaimWithinOneTick`
  (deletes the claim row entirely — the real post-takeover-and-release
  state — runs the tick, asserts the table reads occupied again) and added
  `TestHeldOrderClaimReaffirmTick_CannotEvictALiveTillsLegitimateClaim`
  (seeds a different till's fresh, live claim; asserts the tick's attempt is
  refused and the live till's claim is untouched) — the two properties this
  change actually has and must not have.
- **F4 (major, real — a shipped diff would have produced permanent log
  spam).** Once a held order's table is legitimately held by a different,
  live till (the steady state right after the very outage this card
  addresses), the periodic tick would `Errorf` on every ~30s tick for the
  rest of that order's life — ~2,880 ERROR lines/day per orphaned held
  order, burying real errors. The boot version only logged this once per
  boot, so the periodic version needed different severity, not identical
  behavior. **Fixed**: `reaffirmHeldOrderTableClaims` gained a
  `logRefusalAsError bool` parameter — boot passes `true` (preserving its
  exact original, byte-identical `Errorf` line), the periodic tick passes
  `false` (a bare `claimed=false, err=nil` refusal logs at `Debugf`
  instead; an actual `err != nil` still always logs at `Errorf` on both
  paths).

Non-blocking, noted but not fixed (informational, per reviewer's own
"don't block on these"):

- A small, bounded race between the tick's `List` and its per-table claim
  if an operator resumes/releases/moves a held order concurrently — already
  an accepted class of race on this sync architecture (`hold_api.go`
  documents leaked claims on a similar path as "silent and durable"); on a
  replica it self-heals via the existing TTL/boot mechanisms, on a
  primary/standalone till it's a pre-existing risk class, not a new one in
  kind.
- A scale bound: sequential per-table claims at an 800ms client timeout
  mean ticks coalesce past ~37 concurrently parked orders and could exceed
  `tillClaimTTL` past ~150 — not a concern at pilot scale.

## Not in scope

- No change to `tillClaimTTL` itself.
- No retry/reconciliation for `hold_api.go`'s already-logged primary-side
  release failure on table-move (a separate, smaller gap noted in the
  original card, explicitly deferred there).
- No ADR — this is a bug-fix-shaped extension of the already-accepted
  ut-docs#1703/#1704 write-through design, not a new architectural
  decision.

Closes universaltill/ut-docs#1724.
