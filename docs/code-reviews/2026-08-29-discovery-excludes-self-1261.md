# Code review — "Join an existing shop" LAN scan can return itself (ut-docs#1261)

- **Date:** 2026-08-29
- **Branch:** `fix/1261-discovery-excludes-self`
- **Reviewer:** independent reviewer (Opus, this pipeline's `complexity:medium`
  review tier — Sonnet wrote it, Opus reviewed it, per the `reviewer` skill),
  isolated worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings. Two non-blocking cleanups
  found by review were fixed here (not deferred); one informational note is
  recorded below rather than acted on.

## What shipped

Any till currently in primary/standalone mode advertises itself over mDNS
(`discovery.Advertiser`) — which is every freshly set-up till by default
(`discovery.RoleCheckFromSettings`). Its own "Join an existing shop" LAN
scan (`GET /api/setup/discover-primaries`, and its manager-gated sibling
`GET /api/sync/discover-primaries`) could therefore return its own
advertisement as a candidate primary to join. Joining yourself makes no
sense and the pairing attempt always fails — confirmed live on Pi5-1
(192.168.1.163) in the ticket, whose own scan showed itself as a result.

The fix, in `discoverPrimariesHandler` (`internal/pages/discovery_api.go`):
after `discoveryBrowse` returns candidates, look up this till's own
`till_id` via `discovery.TillID` (the same stable identity
`pairing_api.go`/`pending_pairings.go` already use for pairing verification
codes) and drop any candidate whose `till_id` matches it, before encoding
the response. Both registered routes share the one handler, so both are
covered; the gate (`managerGate`/`firstBootGate`+rate-limit) still runs
first and is unaffected — `d *common.Deps` is just an added parameter, not
a change to gate wiring.

Fail-open by design: if the own-id lookup itself errors, the handler logs
it and returns the unfiltered list rather than 500ing an otherwise-successful
scan — the candidate list is derived entirely from mDNS broadcasts already
public to every LAN host, so failing open leaks nothing beyond what pre-fix
behaviour already showed.

## Independent review — what was checked

- **Gates, all real output, all green** (re-run personally in an isolated
  worktree, not taken from the implementer's report): `gofmt -l` (empty),
  `go build ./...`, `go vet ./...`, `go test ./internal/pages/...
  ./internal/discovery/...`, `-race` scoped to the touched tests, and the
  **full repo test suite** (`go test ./...`, 40 packages, all ok).
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh` all exit 0.
- **TDD claim independently re-verified**, not taken on trust: reverted only
  `discovery_api.go` to `main`'s content (test file untouched), re-ran
  `TestDiscoverPrimariesAPI_ExcludesOwnAdvertisement` — it compiled and
  failed for real (`expected only the other till's candidate, got:
  [{TillID:<own-uuid>} {TillID:till-other-1}]`, i.e. exactly the bug).
  Restored the fix; all tests green again.
- **`till_id` comparison is correct and sufficient**, verified against
  source rather than assumed: `advertiser.go` broadcasts `id=` from the
  same `discovery.TillID`, and `candidateFromEntry` (`browse.go`) already
  rejects any entry with an empty `id` outright — so no empty-TillID
  candidate can reach the filter, and even a degenerate empty `myID` would
  make the filter a safe no-op rather than a false drop.
- **Repository pattern**: `data.NewSettingsRepo(d.Db)` is the same
  established call-site pattern already used in `pairing_api.go:175` and
  `pending_pairings.go:30` — no new raw SQL, nothing new to the data layer.
- **Recurring bug classes checked, both genuinely N/A**: no file writes in
  this diff (no `os.MkdirAll` question) and no filesystem paths at all (no
  `paths.Data(...)` question).
- **No UI/i18n surface touched** (API-only diff) — the UX/i18n checklist and
  the user-manual-freshness check don't apply; confirmed the manual
  (`web/help/en/multitill.md`) already just says "pick the main till from
  the list" and never implied you'd see yourself, so nothing there needed
  updating either.
- No real client/shop name or secret-shaped literal in the diff (test data
  is "My Store"/"Task Runner"/`till-other-1`, consistent with existing
  fixtures already in this file).

## Findings

1. **Fixed — in-place filter aliasing (`candidates[:0]`).** `Browse`
   allocates a fresh slice per call today, so this wasn't a live production
   bug, but filtering into the same backing array is a foot-gun for any
   future caller reusing a candidates slice across requests (reviewer
   demonstrated it corrupting a second call's stubbed data in a throwaway
   probe). Changed to `filtered := make([]discovery.Candidate, 0,
   len(candidates))`.
2. **Fixed — the `discoveryTillID` seam was unused.** It existed
   (mirroring `discoveryBrowse`'s established test-seam pattern) but no
   test exercised the fail-open branch it exists for. Added
   `TestDiscoverPrimariesAPI_FailsOpenWhenOwnTillIDLookupErrors`, which
   stubs `discoveryTillID` to fail and asserts a 200 with the unfiltered
   candidate list (not a 500).
3. **Informational, not acted on** — `discovery.TillID` is get-or-create,
   so the unauthenticated `/api/setup/discover-primaries` route can now
   persist a fresh uuid on a till that has never advertised before. Bounded
   and acceptable: idempotent, already rate-limited to 5/min
   (`setupDiscoverLimiter`), the value is a locally-generated uuid never
   attacker-influenced, and on any till that's actually advertising (the
   only case where this filter changes behaviour) `discovery.Advertiser`
   already created the id at startup — so that path only ever hits the
   cheap `Get`, not the write.
4. **Test-depth judgement, endorsed not changed** — stubbing `Browse`
   rather than driving a real multi-advertiser mDNS scenario is the right
   level for this fix: the bug is a filtering omission in the handler, the
   mDNS transport layer already has its own coverage in
   `internal/discovery`, and a real multicast integration test would be
   flaky in CI. Added
   `TestDiscoverPrimariesAPI_AloneOnLANSeesEmptyArrayNotItself` to pin the
   ticket's literal acceptance-criteria scenario (a standalone till alone
   on the LAN sees an empty array, not itself, not `null`) directly,
   rather than relying only on the two-candidate exclusion test to imply it.

## Manual verification beyond automated tests

This is a backend-only API fix with no UI surface and no real multi-device
LAN available in this session (mDNS multicast needs real network
adjacency) — driving it "for real" beyond the handler-level `httptest`
calls against a real SQLite-backed `common.Deps` (which do exercise the
actual `discovery.TillID`/settings-repo code path, not a mock of it) isn't
meaningfully achievable in a cold cloud session. This gap is noted rather
than hidden; the acceptance criteria's own regression-test requirement is
met by the tests above, and the live evidence in the ticket (the two-entry
scan result captured from Pi5-1) is what originally proved the bug and what
the fix removes.
