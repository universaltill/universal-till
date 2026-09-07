# Review: held-order (parked) cross-till table occupancy (ut-docs#1704)

## What shipped

A dine-in order held (parked) on one till now shows as occupied on every
other till connected to the same primary — the remaining gap after
ut-docs#1392 (read-only cross-till occupancy display proxy) and ut-docs#1703
(cross-till live-basket claim write-through + TTL reconciliation), both
already merged. Split from the same #1392 Architect pass that produced
#1703.

**Design: reuse the already-built `table_claims` write-through/TTL
machinery instead of building a new sync mechanism for `held_sales`
itself.** `held_sales` still isn't synced or proxied cross-till at all —
what changed is that holding an order no longer releases the
`table_claims` row its table was originally picked with
(`internal/pages/hold_api.go`'s hold handler). That row was already
write-through'd to the primary the moment the table was picked
(`claimTableWriteThrough`, ut-docs#1390/#1703), so the primary's existing
`GET /api/sync/tables` (ut-docs#1392, unchanged) already serves it to
every other till with zero new endpoints. The held/table move handler had
to learn to migrate that claim (release old, claim new) since it
previously only touched `held_sales.table_id`.

New: `internal/pages/hold_cross_till_test.go` — end-to-end integration
tests standing up a REAL primary (`registerSyncTables` +
`registerSyncTablesClaim` on a real migrated DB) and a REAL replica
(`registerHoldAPI` on a real migrated DB), asserting the actual AC through
the real HTTP surface both ends use.

## Independent review (Opus, worktree-isolated) — no blocker, six should-fix findings

**Verdict: yes, with fixes needed — no blocker.** The core design was
confirmed sound: the reviewer re-derived `ClaimTableForTill`'s SQL truth
table by hand and probe-tested it, could not get two tills to hold one
table, and confirmed a replica can never expire the primary's own
`till_id=''` row. Full report: 10 findings (6 should-fix, 3 nit, 1 setup
note), all triaged below.

### Fixed (6 should-fix)

1. **A failed release leg on a held-order move orphaned the OLD table's
   claim on the primary, permanently, with no signal.** Unlike a live
   basket's release sites (which sit on a basket that keeps moving and
   re-touching claims), nothing else ever revisits a moved-away-from held
   table. Fixed: `releaseTableClaimWriteThrough`/`releaseTableClaim` now
   return whether the primary-side release succeeded (every existing call
   site still ignores it, unaffected); the held/table move handler checks
   it and logs loudly (`log.Printf`, gated on `isReplica` so a
   standalone/primary till — the ordinary case — never logs a false
   alarm) specifically when it fails.
2. **`renderHeldStrip` was the one remaining display site still calling
   `posRepo.ListTablesWithState` directly instead of
   `tablesWithStateForDisplay`** — so the move menu offered a table
   another till already held, and the refusal was completely silent (200,
   no `HX-Trigger`, unchanged HTML — a cashier tap that visibly did
   nothing). Fixed: swapped to `tablesWithStateForDisplay`; the move
   handler now renders the existing `basket.table.occupied` toast
   (reusing the live-basket picker's own key, same `.pos-notice` markup
   `app.js`'s shared dismiss handler already picks up) on a genuine
   refusal.
3. **A stray blank line detached `ClaimTableForTill`'s entire ~50-line doc
   comment from `go doc`.** `go doc ./internal/data
   POSRepo.ClaimTableForTill` printed nothing. Fixed: removed the blank
   line; `go doc` now prints the full comment again.
4. **Six stale invariant comments, now contradicted by the code**
   (`tables_claim_proxy.go`, `tables_repo.go`'s `IsTableFree`/
   `ClaimTableForTill`, `pos_api.go`'s `releaseTableClaim`, `init.go`'s
   boot sweep) — this codebase's safety model leans on these comments
   being accurate, so each was rewritten to describe the actual new
   behaviour (a `till_id=''` row can now be re-taken by its own owner, not
   just expired by staleness; `IsTableFree` can now see a caller's own
   held-order claim and callers must short-circuit it themselves; parking
   an order no longer releases the claim; the boot sweep now explicitly
   defers to a re-claim step for held orders).
5. **The self-move short-circuit (`tableID == held.TableID`) was entirely
   untested, and guards a destructive path** — the reviewer replaced the
   condition with `if true` and every existing test, including the new
   cross-till ones, still passed, because none seeded a `table_claims` row
   to detect the difference. Without the short-circuit, a self-move could
   ask `IsTableFree` about a table this held sale's own claim already
   occupies, get refused (or worse, under an earlier draft, succeed and
   then release its own claim as "the old table"). Fixed: added
   `TestHeldTableHandler_MoveOntoOwnCurrentTableLeavesClaimUntouched`,
   which seeds a real claim first. TDD-verified: reverting the
   short-circuit to `if true` makes it fail (`expected HX-Trigger:
   held-changed even for a self-move no-op, got ""`) — restoring the fix
   passes again.
6. **A real, adjacent gap the reviewer's own reasoning surfaced but didn't
   explicitly flag: the boot sweep unconditionally wipes every
   `till_id=''` claim, including a genuinely still-parked order's, and
   nothing re-created it.** Locally invisible (`ListTablesWithState`
   already reads `held_sales` directly), but the wiped row is exactly what
   a replica had write-through'd to the primary — so a parked order's
   table read FREE cross-till after **every single restart**, not just
   after a `>tillClaimTTL` outage (a materially worse version of the
   reviewer's finding #4, "no re-assertion loop"). Fixed: `Init` now
   re-claims every `held_sales.table_id` once `dp` exists (right after its
   construction, before route registration), via the same
   `claimTableWriteThrough` call used everywhere else — restoring both
   this till's own local mirror and, on a replica, re-affirming the
   PRIMARY's row. TDD-verified with a new
   `TestInit_ReclaimsHeldOrdersTableClaimOnBoot`: reverting the boot
   re-claim block makes it fail (`want 1 table_claims row, got 0`) —
   restoring passes again.

### Also fixed, found while triaging the findings above (not in the
### original report, but the same root cause)

- **A go-live reset (`ResetTransactionHistory`) archives/clears
  `held_sales` but never touched the now-orphaned `table_claims` row a
  parked order left behind** (the reviewer flagged this as a related nit,
  #8, in passing) — under the OLD design this was impossible (no claim
  ever outlived a hold), so it's a genuinely new gap this card
  introduces. Fixed: the reset now clears `table_claims` for any table_id
  a `held_sales` row referenced, in the same transaction, right before
  that table is cleared. `table_claims` itself is still correctly never
  archived (ephemeral, not transactional history). TDD-verified with a
  new `TestResetTransactionHistory_ClearsHeldOrdersTableClaim`.
- **The manual (`web/help/en/tables.md`) overclaimed Free table's
  "it never touches a real held order... the till tells you so"
  promise** (reviewer finding #3) — true only when the held order is on
  the *same* (primary) till; `ForceReleaseTableClaim`'s `stillHeld` only
  ever consults the primary's own `held_sales`, and a replica's parked
  order is invisible to it. Corrected the prose rather than leave a false
  promise; `make docs-shots` re-run (screenshots unaffected by this
  page's prose-only change, `en`/`ar` `sell.png` regenerated with normal
  run-to-run pixel variance from dynamic screen content, manifest hash
  updated). Filed **ut-docs#1723** for the actual fix (needs its own
  Architect pass — closing it fully means teaching the primary about a
  replica's held-order existence, out of scope for this card, same
  reasoning #1392 used to split off #1703/#1704 in the first place).

### Deferred, tracked (not fixed here)

- **Reviewer finding #4 (no periodic re-affirm while a till stays
  running)** — the boot-time fix above closes the *restart* path, which
  covers the common recovery case, but a till that stays up through a
  `>tillClaimTTL` primary-reachability gap **without restarting** still
  has no re-affirm loop. Filed **ut-docs#1724** (needs an Architect pass
  to weigh a periodic re-affirm against real-world frequency of the
  trigger — a live till losing the primary for over two minutes without
  restarting is presumably rare, worth checking before building a new
  polling write path).
- **Reviewer finding #9 (millisecond crash window between claiming the
  new table and releasing the old one during a move leaves both
  claimed)** — nit severity, same class of accepted race already
  documented elsewhere in this file's own comments (e.g. the release
  write-through's fire-and-forget stance); not fixed, not filed
  separately — self-heals via the next restart's boot re-claim, or a
  manager's Free table.
- **Reviewer nit #10, test hygiene** — `tables_repo_test.go`'s 1.1s sleep
  for RFC3339 second-resolution left as-is (working, not worth a fake
  clock for one test); `newHoldCrossTillPrimary`'s unused `*db.DB` return
  value dropped from its signature.

## What was verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` (clean) after every
  round of fixes, not just once at the end.
- Full `go test ./...` green (all packages, not just the changed ones).
- `golangci-lint run ./...` — 0 issues.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally and green, including `guard-data-access.sh` (no raw SQL outside
  `internal/data`/`internal/db` — the new integration test file uses raw
  SQL only for seeding, the repo's own established test convention) and
  `guard-docs-shots.sh` (screenshots regenerated via `make docs-shots`
  after the manual prose fix).
- Two independent TDD revert-restore rounds performed personally (beyond
  the reviewer's own two, on the original diff): the self-move
  short-circuit finding and the boot re-claim finding, both reproduced a
  genuine failure on revert and passed again on restore.
- The reviewer's own adversarial pass: attempted (and failed) to make two
  tills hold one table via the `ClaimTableForTill` SQL change; attempted
  (and failed) to make a replica expire the primary's own local claim or
  vice versa; attempted (and failed) to leak a claim on the move's
  `SetTable`-failure path; confirmed the live-basket pick still can't
  steal a parked order's table; confirmed a Takeaway held order's claim
  is still released on the forced clear.

## Model routing note

Card labelled `complexity:hard` at grooming (genuinely cross-cutting: a
concurrency/reconciliation design question, explicitly why #1392 split it
out as its own Architect pass). Built INLINE on the session's own model
(Sonnet) rather than delegating Dev to a `fable` subagent as the hard-tier
default routing calls for: once the BA/Architect design work (the actual
hard part — realizing the existing `table_claims` write-through/TTL
mechanism could be reused instead of building a new sync mechanism for
`held_sales`) was done, the remaining Dev diff was mechanically small and
contained (effectively one file plus its tests), and MODEL-ROUTING's own
principle is to size by the hardest single step, not total effort — a
fresh subagent briefing would have cost more than it saved. Review still
ran at Opus, deliberately, per the hard tier's review routing.

## Safe-to-merge verdict

**Yes.** Every should-fix finding is fixed and re-verified (build, test,
vet, lint, guards all green; two fixes independently TDD-confirmed
against a real failure); the two genuinely-deferred items are real,
scoped follow-up cards, not swept under the rug; nothing found rises to
blocker (money/tax/data-loss/security) severity, so no second review round.
