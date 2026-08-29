# Code review: gate mDNS advertising on first-boot completion (ut-docs#1263)

**Date:** 2026-08-29
**Author:** Farshid Mirza (pipeline, lane:cloud-54)
**Reviewer:** independent subagent, Opus, fresh context, isolated worktree

## What shipped

`internal/discovery/advertiser.go`'s `RoleCheck` rule — "empty
`sync.primary_url` setting means primary, so advertise" — is equally true
of a till that has simply never been set up yet. A freshly wiped/flashed
device started advertising itself over mDNS as a candidate "primary"
within seconds of boot, before a human had ever opened the setup wizard —
confirmed live on Pi5-1 (ut-docs#1199/#1261). During a realistic rollout
(several devices unboxed and booted in a row), each transiently appeared
as a phantom join target in anyone else's "join an existing shop" LAN
scan, with no way to tell which one was the operator's intended primary.

- **`internal/discovery/discovery.go`** — new `FirstBootChecker` interface
  (narrowed seam matching `*auth.Service.NeedsFirstBoot`, same pattern as
  the existing `RoleCheck`/`mdnsServer` seams) and
  `GateOnFirstBoot(inner RoleCheck, firstBoot FirstBootChecker) RoleCheck`:
  short-circuits to "don't advertise" without calling the checker when
  `inner` already reports not-primary; otherwise calls `NeedsFirstBoot`
  and advertises only when setup is complete; fails closed (withhold) if
  the check itself errors.
- **`internal/app/app.go`** — wires it: `auth.NewService(database.DB)`
  constructed at the point the discovery advertiser is built (mirrors the
  existing "don't wait for `pages.Init`'s deps" reasoning already
  documented there for the settings-based role check), composed as
  `discovery.GateOnFirstBoot(discovery.RoleCheckFromSettings(discoverySettings), discoveryAuth)`.
- **`internal/discovery/discovery_test.go`** — 6 new tests: a 4-case truth
  table over (inner primary/not-primary × firstBoot needed/not-needed),
  fail-closed-on-error, and a short-circuit test asserting the checker is
  never invoked once `inner` already reports not-primary.

## Independent review (Opus, isolated worktree)

**Verdict: SAFE TO MERGE. No blocking findings.**

Confirmed:
- The chosen signal (`NeedsFirstBoot`) is the same predicate already
  gating every other first-boot-only surface in this codebase
  (`api_gates.go`, `setup_page.go`, `setup_language_catalog.go`,
  `setup_tax_catalog.go`, `sync_api.go`, `auth_page.go`) — not an ad-hoc
  invention, so the invariant this enforces ("discoverable exactly when
  no longer in the first-boot window") is the established one.
- Per-tick cost is a non-issue: `NeedsFirstBoot` → `ListActiveUsersWithPIN`
  is a plain read, no write lock, on a background 30s-interval goroutine —
  does not reintroduce the write-lock-on-every-poll pattern `TillID`'s own
  comment warns against.
- Boot ordering is safe: migrations run before the advertiser is wired.
- A second `auth.Service` is cheap (two struct allocations, no shared
  mutable state) — no interface/mock needed elsewhere.
- Composition is fail-closed and self-healing: a transient error withholds
  advertising for at most one 30s tick, then re-evaluates from scratch —
  no latching state, so no "silently stops forever" risk.
- Edge case checked against the documented pairing flow
  (`web/help/en/multitill.md`): a mid-wizard till correctly never needs to
  be discoverable — the join target is by definition already set up
  (a manager shows the pairing code from an already-configured till's
  Settings → Tills page).
- Neither of the two recurring bug classes this pipeline watches for
  applies (no file I/O, no path handling in this diff).
- TDD claim independently re-verified: revert-only-the-production-files →
  `go test ./internal/discovery/...` fails to compile
  (`undefined: GateOnFirstBoot`, 3 sites) → restore → passes clean. Tests
  genuinely depend on the new code, not a false pass.
- Ran clean: `gofmt -l`, `go build ./...`, `go vet ./...`,
  `go test ./internal/discovery/... ./internal/app/... -race -v` (no race
  warnings), `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`.

**Non-blocking notes (accepted, not fixed — recorded per triage):**
1. `UT_AUTH=off` doesn't bypass the gate (only the HTTP middleware is
   skipped) — a dev/CI till with no seeded PIN user will now never
   advertise. Dev-ergonomics footgun only; every real till runs auth on,
   and `e2e/tests/tills-lan-discovery.spec.ts` already tolerates both
   terminal states.
2. A transient check error tears the mDNS server down rather than merely
   pausing a start — self-healing on the next 30s tick is adequate.
3. `NeedsFirstBoot` materializes full user rows (incl. `pin_hash`) just to
   test `len(users) == 0` — pre-existing, not introduced here; this diff
   makes it a recurring 30s call, so a `COUNT`/`EXISTS` repo method would
   be cheaper if it's ever touched for other reasons.
4. No test asserts `app.go` itself composes the gate correctly — same
   pre-existing gap as `RoleCheckFromSettings`'s own wiring had.
5. Benign `WARN ... context canceled` log line possible on shutdown mid-
   tick, consistent with existing shutdown noise elsewhere in this app.

None of the above block merge; (1) and (3)/(4) are reasonable future
Backlog candidates if they ever cause real friction, not filed as new
cards given how minor they are.

## Verification (personally re-run before commit)

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | clean |
| `go test ./internal/discovery/...` | pass (6/6 new tests) |
| `go test ./...` (whole repo) | pass |
| `-race` scoped run (`internal/discovery`, `internal/app`) | pass, no warnings |
| TDD red→green | independently re-verified twice (by the implementing session, then again by the independent reviewer) |
| All 16 CI-blocking guards (`scripts/ci/*.sh`, `check-brand-assets.sh`) | all pass |

Deliberately out of scope, not silently dropped: the two non-blocking
dev-ergonomics/perf notes above; no manual/help-topic update needed since
this is a background-service behavior change with no shop-owner-visible
surface.
