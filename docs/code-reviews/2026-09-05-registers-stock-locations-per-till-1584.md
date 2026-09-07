# registers/stock_locations: decide shop-wide-vs-per-till sync (ut-docs#1584)

**Card:** universaltill/ut-docs#1584 — split from #1554. #1554 shipped the
security-relevant `role_permissions` sync fix; this card was the
remaining "undecided" pair from its acceptance criteria: does `registers`
or `stock_locations` need to start syncing shop-wide, or stay per-till?
**Repo/branch:** universal-till, `fix/1584-registers-stock-locations-per-till`
**Complexity:** medium (Dev at Sonnet, Review at Opus — one round, no
blocker, four non-blocking nits fixed in-session).

## The decision

**Both `registers` and `stock_locations` stay PER-TILL** (neither added to
`internal/data/sync_admin_repo.go`'s `adminTables`) — a traced decision,
not the guess the previous comment amounted to.

The trace: `POST /api/registers` and `POST /api/locations`
(`internal/pages/registers_page.go`, `locations_page.go`) are gated only
on the `stock_location_management` permission — reachable from **any**
till, not primary-only (there's an existing primitive for that,
`d.SyncPrimaryURL(r.Context()) == ""`, used in `settings_page.go`'s
promote-only sections, but these two pages don't use it). `ApplyAdmin`'s
one-way "primary wins" pull (`deleteMissing`) only protects a row from
outright deletion if the DB's own FK blocks it (existing shift/sale/
inventory history already references it) — a freshly satellite-created
register or location has no such history yet, so nothing would stop it
being silently wiped on that satellite's very next admin pull from a
primary that never heard of it. That's a real correctness regression, not
hypothetical, and the same failure class #1554 fixed for
`role_permissions` — but `role_permissions` had a `skipPrune` carve-out
(`rolePermissionSkew`, ut-docs#1589) to fall back on; registers/
stock_locations have no equivalent today.

Registers' shop-wide need is separately already met at enrollment time:
`CreateRegisterForEnrolment` runs on the **primary**
(`POST /api/sync/enroll`), so a joining till's own register exists
shop-wide before its first sale via the initial full-DB-snapshot join,
not via ongoing sync.

Filed **ut-docs#1590** for the real follow-up: gate `/registers` +
`/locations` creation to primary-only (reusing the existing
`SyncPrimaryURL` primitive), then flip both tables to sync with
round-trip tests, matching #1554's `role_permissions` shape.

## What shipped

- `internal/data/sync_admin_repo.go`'s `adminTables` top comment rewritten
  to state the decision and the actual trace above (replacing the
  previous "still an OPEN decision" placeholder #1554 left behind).
- `internal/data/sync_admin_repo_test.go`: new regression test
  `TestAdminApplyLeavesRegistersAndStockLocationsUntouched`, which locks
  in both halves of the decision — (1) neither table is ever dumped into
  an admin bundle, (2) a satellite's own locally-created register/
  location survives an admin pull from a primary that's never heard of
  it — plus a one-line comment fix to a pre-existing test
  (`TestAdminDumpApplyRoundTrip_TillRegisterIDNeverSyncs`) whose wording
  had gone stale against this decision.
- No `adminTables` entries added — the acceptance criteria's "if either
  flips to sync: added with round-trip tests" is correctly not triggered,
  since neither flips.

## Independent review — one round, Opus, no blocker

Spawned as a worktree-isolated `general-purpose` subagent (`model: opus`)
against a WIP commit. Independently re-verified every factual claim in
the trace against the actual code (not taken on my word) — the
permission gates, the FK/UNIQUE schema, `deleteMissing`'s fallback logic,
and the enrollment flow — all confirmed. Ran the full gate itself
(`gofmt`/`go build`/`go vet`/targeted + full `go test`/
`guard-data-access.sh`/`guard-i18n.sh`), all green.

**TDD-style verification, independently re-run.** Reverted the decision
by adding both tables back into `adminTables`, confirmed
`TestAdminApplyLeavesRegistersAndStockLocationsUntouched` fails with the
exact silent-wipe symptom the decision is meant to prevent
(`sql: no rows in result set` — the satellite-created row is gone),
re-ran with only `registers` added to isolate that half independently,
then reverted and confirmed clean again. (I'd already done this same
check myself before commit; the independent re-run reproduced identical
results.)

**Scope check beyond the original trace.** Reviewer looked specifically
for any code path where a satellite's own register/location must be
visible shop-wide for correctness (which would make per-till the wrong
call) — traced the full FK graph, the sales journal
(`internal/pages/sync_sales.go`), stock sync D3b, and the one
register+location reporting join (`FiscalRegisterDEStore.List`). None
exists: replica-sourced sales already land on the primary with
`register_id = NULL` regardless of this decision (`applyJournal` never
sets it), so syncing `registers` wouldn't even enable per-register
attribution of satellite sales — this *strengthens* the per-till call
rather than merely failing to contradict it.

**Non-blocking, fixed in-session (4 nits):**
1. Comment misattributed the mechanism that protects `role_permissions`
   from the equivalent bug — said "`SetRolePermission`'s always-upsert
   semantics"; the actual analogue is the `rolePermissionSkew` skipPrune
   carve-out (ut-docs#1589). Corrected.
2. Comment referenced `registers.CreateRegister/CreateStockLocation` as
   if `registers` were a package; both are `POSRepo` methods. Corrected
   to `POSRepo.CreateRegister`/`CreateStockLocation`.
3. New test reused one `name` variable across two `Scan` calls — a
   register-assertion failure would report the stock location's value
   instead (reviewer reproduced this exactly while isolating the two
   halves). Split into `locName`/`regName`.
4. A pre-existing test's comment ("Same shop-wide registers table on both
   sides, as a real admin sync would produce") now asserts the opposite
   of this card's decision. Reworded to say registers doesn't admin-sync
   and the test seeds both sides by hand to simulate the shop-wide roster
   a real till would already have from its initial join.

None of the four change the decision or require re-scoping; all fixed
directly, re-gated after (`gofmt`/`go build`/`go vet`/
`go test ./internal/data/... -run TestAdmin -v`: 21/21 pass; full
`go test ./...`: all packages green; `golangci-lint run
./internal/data/...`: 0 issues).

**Noted, not fixed (cosmetic, doesn't change the decision):** the
comment's claim that "a joining till's own register already exists
shop-wide" holds for the primary and that joining till, but a till that
joined *earlier* never sees a register added later, since registers
don't sync — an incomplete `/registers` list today. Worth remembering if
someone later revisits this decision; not worth a comment caveat that
would make an already-long paragraph longer for a corner case with no
user-visible report behind it.

## Verified beyond automated tests

- `gofmt -l .`: clean (both changed files). `go build ./...`,
  `go vet ./...`: clean. `golangci-lint run ./internal/data/...`: 0
  issues.
- `go test ./internal/data/... -run TestAdmin -v`: 21/21 pass. Full
  `go test ./...`: every package green (no `-race` timeout hit this run).
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh`: both pass — expected, since this diff
  touches only Go comments and a Go test file, no SQL outside
  `internal/data`, no user-facing strings, no template/UI surface.
- No migration touched — ADR-0074's append-only rule isn't engaged; this
  changes only which tables sync, not the schema.
- No UI/help-topic/compliance-wording surface touched — those checklists
  correctly don't apply.
- No real client/shop name in test data (`Primary HQ`, `Front Till`,
  `Satellite Pop-up Store`, `Pop-up Till` — all generic). No
  secret-shaped literal anywhere in the diff.

## Safe to merge

Yes. No blocker. Four non-blocking nits, all fixed in-session. One new
Backlog card filed for the actual follow-up (ut-docs#1590 — gate
`/registers`+`/locations` to primary-only, then sync both with round-trip
tests) rather than folded into this card, since it's a real design change
(a new UI-visible gate + behavior flip), not a one-line addition.
