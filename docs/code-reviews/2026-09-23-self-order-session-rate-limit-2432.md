# Review: self-order session-mint rate limit + count cap (ut-docs#2432)

**Card:** ut-docs#2432, `complexity:medium`, `security`. Follow-up (finding
N1) from the ut-docs#2261 review
(`docs/code-reviews/2026-09-19-self-order-session-basket-2261.md`):
`pos.SessionBasketManager.Create` had no cap on `len(m.sessions)`. `GET
/self-order?table=<validID>` mints a new session on every cookieless hit
the busy guard doesn't block, and that route is auth-exempt/LAN-reachable
by anyone — a request loop against one table's QR could allocate sessions
at line rate, a real OOM path on a Pi-class till.

**Process:** the fix (branch `fix/2432-self-order-session-rate-limit`) was
built by an earlier, since-disabled cloud lane run and left as an orphaned
branch with no PR for 4 days (`:24` routine disabled 2026-09-20, no
activity since 2026-09-19T10:53Z). This cycle's scrum-master sweep found
it via the stale-`status:in-progress`-label check (SKILL.md lane rule 6),
reset the card to Ready per PR-SWEEP.md, then picked it back up rather
than redo the work — the branch's diff and tests were sound and worth
keeping. Independent review at **Opus** (this card's Dev tier is Sonnet,
so review runs one tier up per `MODEL-ROUTING.md`), full diff, own
build/vet/lint/test run (not trusted from the branch).

## What shipped

- `internal/pages/self_order_page.go`: a `pairRateLimiter` (the same shape
  `internal/pages/api_gates.go`'s `rateLimited` already uses for the
  identical anonymous-LAN-route threat class) caps new-session **minting**
  per source IP at 20/minute. Only gates an actual mint attempt — never the
  resume-by-cookie or table-owner-busy branches.
- `internal/pos/session_manager.go`: `MaxLiveSelfOrderSessions = 500` caps
  the manager's total live-session count; `Create` now returns
  `(string, *Service, bool)`, `ok=false` meaning nothing was created.
- Both refusals (limiter, cap) reuse the existing "table busy" screen —
  no new UI state or i18n key for the *original* fix.

## Independent review — round 1

Ran `go build ./...`, `gofmt -l .`,
`golangci-lint run ./internal/pages/... ./internal/pos/...`,
`go test ./internal/pos/... -race`,
`go test ./internal/pages/... -run SelfOrder -race`,
`guard-kiosk-engine.sh`, `guard-data-access.sh` — all green. A mutation
test (limiter disabled in a scratch worktree) confirmed the new rate-limit
test fails for the right reason.

**Findings:**

| # | Severity | Summary | Outcome |
|---|---|---|---|
| 1 | **High** | The cap alone turns the memory bug into a guaranteed denial of service: one source mints ~20 empty sessions/minute — well within the limiter's own allowance — filling `MaxLiveSelfOrderSessions` in ~25 minutes. Sessions only leave via the 2h idle sweep, so one device can keep the cap full indefinitely, and once full `Create` refuses *every* source, at *every* table, not just the attacker's. No IP rotation or bypass needed. | **Fixed**, see below |
| 2 | Medium | A rate-limited/capped guest sees the same copy as a genuinely busy table ("Someone else at this table is already ordering…") — misleading, and on NAT'd guest Wi-Fi the limiter's per-IP counting hits real guests too. A distinct "try again shortly" message would need a new i18n key across all locales (lang-pack follow-up). | Deferred: ut-docs#2490 (filed this session) |
| 3 | Low | Doc comment on `bindSelfOrderTableSession` claimed both the limiter and the cap run before the old session is removed; only the limiter did — the cap check ran after `Remove(currentToken)`, so a table-switching guest could lose their old session on a refused mint. | **Fixed**, see below |

Also explicitly checked and confirmed clean: no TOCTOU race between the
limiter check, the cap check and the map insert (single `m.mu`-held
critical section in `Create`); resume-by-cookie, the table-owner busy
check, and the idle sweep are all unaffected by either gate;
`sourceOf(r)` reads `r.RemoteAddr` (not a spoofable header — sound for a
LAN-only route); no filesystem writes in this diff (the two recurring bug
classes — missing `os.MkdirAll`, a cwd-relative path — don't apply); the
busy-screen template/i18n keys are byte-for-byte unchanged, so no
`web/help/` update is owed for *this* fix (finding 2's deferred follow-up
will owe one when it lands).

## Fix (findings 1 and 3)

- **Finding 1** — `SessionBasketManager.Create` now evicts the
  least-recently-seen **empty** session (no items in its basket) to make
  room at the cap, mirroring the "an abandoned/empty cart has nothing to
  lose" logic the busy guard already applies
  (`TableOwnerActive`/`len(owner.Lines()) > 0`). `Create` only genuinely
  refuses when every one of the 500 live sessions already holds real order
  items — the true memory bound the cap exists to enforce. An attacker
  filling the map with empty sessions now only ever evicts its *own*
  sessions; a real guest at any table is unaffected. New tests:
  `TestSessionBasketManager_CreateAtCapEvictsOldestEmptySession`,
  `TestSessionBasketManager_CreateRefusesOnlyWhenEveryLiveSessionHasItems`
  (renamed/rewritten from the branch's original `..._CreateRefusesAtCap`,
  which asserted the now-fixed bad behavior),
  `TestSelfOrder_MapFullOfEmptySessions_NewGuestAtDifferentTableStillServed`
  (`internal/pages`, end-to-end through the real HTTP handler).
- **Finding 3** — reordered `bindSelfOrderTableSession`: `Create` now runs
  *before* `Remove(currentToken)`, so a refused mint never costs a guest
  their still-live old session. Comment corrected to describe the actual
  order.
- TDD-verified myself: reverted just the `Create` eviction logic (restored
  the branch's original unconditional cap-refuse), confirmed
  `TestSessionBasketManager_CreateAtCapEvictsOldestEmptySession` and
  `TestSelfOrder_MapFullOfEmptySessions_NewGuestAtDifferentTableStillServed`
  both fail with exactly the claimed symptom (`Create at the cap must
  still succeed...` / busy screen shown to a fresh guest), restored the
  fix, confirmed both pass again.
- Full gate after the fix: `gofmt -l .` clean, `go build ./...` clean,
  `golangci-lint run ./internal/pages/... ./internal/pos/...` 0 issues,
  `go test ./internal/pos/... ./internal/pages/...` (targeted + `-race`)
  all green, `guard-kiosk-engine.sh` / `guard-data-access.sh` green.

## Merge with `main` (re-integration, not a mechanical resolve)

`main` had moved 66 commits since this branch's point, including three
cards on this exact code (`ut-docs#2433` table-move-preserves-basket,
`ut-docs#2434` table-bind-race, `ut-docs#2444` empty-session-short-busy-
window) that replaced `bindSelfOrderTableSession`'s old
Create-then-Remove-then-SetTable dance with `SessionBasketManager.BindTable`
— a single atomic critical section doing the busy check, the mint-or-move,
and the map insert all under one `m.mu` hold. **`Create()` is no longer
called anywhere in production** (grepped: only `BindTable`'s own inline
mint path and this package's tests still call it) — my mint-time
protections were dead code on the production path after a textually-clean
auto-merge. Re-integrated for real, not just resolved:

- The per-source rate limiter moved from an unconditional check to one
  gated on `mover == nil` (no existing cookie at all) in
  `bindSelfOrderTableSession` — under `BindTable`'s new design, a
  table-to-table move (`ut-docs#2433`) reuses the existing session and
  never grows `len(m.sessions)`, so it was never the mint the limiter
  needs to gate; rate-limiting it would have been a new, unintended
  regression on a legitimate flow this same branch's original fix predates.
- The session-count cap (with its evict-oldest-empty step) moved from
  `Create` into `BindTable`'s own fresh-mint branch, reusing the `m.mu`
  already held continuously since `BindTable`'s busy-check loop — one
  atomic step, not a second lock/unlock.
- **Caught before it shipped, re-reviewing my own integration**:
  `evictOldestEmptyLocked` classified a session as empty via
  `sb.svc.Basket().ItemCount() > 0`. `Basket()` calls `recomputeTotals()`,
  which can invoke a blocking plugin tax/charge-policy ask — and this now
  runs under `m.mu` from inside `BindTable`, the exact lock-scope hazard
  `ut-docs#2443`/`#2444`/`#2449`/`#2435` each fixed elsewhere in this same
  file (serializing every other table's concurrent request behind one
  plugin round-trip). Changed to `len(sb.svc.Lines()) == 0` — a
  lock/copy/unlock with no recompute, matching `BindTable`'s own empty
  check and `HasItems`'s established fix for the identical class of bug.
- Fixed 10 other call sites across `internal/pos/session_manager_test.go`
  that `main` had added against the old 2-return-value `Create()` signature
  (`ut-docs#2443`/`#2444`'s own new tests), and 4 in
  `internal/pages/self_order_table_test.go` calling
  `bindSelfOrderTableSession` without the now-required `limiter` param
  (passed `nil` — none of those tests exercise rate-limiting).
  `go vet ./...` caught all of these; none were a silent behavior change,
  all were compile failures.
- Updated this branch's own busy-screen string assertions
  (`"This table already has an order in progress"`) to the copy `main`
  had since revised (`"This table is already in use"`, `ut-docs#2434`
  review finding B2-successor) — 4 stale assertions would have false-passed
  by testing for a string no longer in the template rather than failing
  outright, since `strings.Contains` on a wrong-but-present busy screen
  still returns true for "busy", just not for the *reason* the test names.

Full gate re-run after the merge: `gofmt -l .` clean, `go build ./...`
clean, `go vet ./...` clean, `golangci-lint run ./internal/pages/...
./internal/pos/...` 0 issues, `go test ./internal/pos/... -race` and
`go test ./internal/pages/... -run SelfOrder -race` both green (all
tests, including the pre-existing `ut-docs#2433`/`#2434`/`#2444` coverage
for the newer `BindTable` behavior — table moves, atomic bind, the
empty-session short busy window), `guard-kiosk-engine.sh` /
`guard-data-access.sh` green.

## Verdict

**Safe to merge** with finding 1 and 3 fixed as above, and the
`BindTable` re-integration verified for real (not just made to compile).
Finding 2 (the rate-limited/capped guest seeing busy-table copy instead
of a distinct message) is real but not a blocker — it's a UX/i18n-scoped
follow-up, not a security or correctness gap, and needs its own
locale-key change across `web/locales/*.json` plus a lang-pack follow-up;
filed as ut-docs#2490 rather than widening this PR.
