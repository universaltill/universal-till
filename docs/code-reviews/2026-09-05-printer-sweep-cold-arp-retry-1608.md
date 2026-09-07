# Printer sweep misses a printer on a cold ARP cache (ut-docs#1608)

## What shipped

`internal/discovery/sweep.go`'s `sweepListeners` (phase 1 of `SweepPrinters`)
now retries, once, every host that failed to answer on the first pass —
implementing option 1 from ut-docs#1608 ("retry the misses once"), the
product owner's own recommendation.

**Why:** phase 1 dials all ~253 addresses of the till's own /24
concurrently (64 in flight, 700ms per-host budget). For a host whose ARP
entry is cold, that 700ms has to cover ARP resolution *and* the TCP
handshake, and the sweep's own 250+ simultaneous dials flood ARP
resolution — so a printer that is genuinely online can lose that race and
get missed, worst-case on a till's first boot when every host's ARP entry
is cold. By the time the first pass finishes, a missed host's own traffic
has warmed its ARP entry, so a second dial against only the misses no
longer races the whole subnet for that host.

Also fixed as a direct consequence, found during independent review (see
below): `internal/pages/kitchen_stations_page.go`'s `discoverPrintersTimeout`
(12s) and its comment were stale the moment the retry landed — the retry
roughly doubles phase 1's worst-case latency (~2.8s → ~5.6s, since on a
typical LAN the "misses" are close to the whole subnet, not a handful),
pushing the sequential worst case (phase 1 + the two ESC/POS probe passes)
to ~13s against a 12s deadline. Bumped to 20s with the comment's math
corrected, so `DiscoverPrinters` doesn't itself start silently truncating
scans — the same "my printer isn't in the list" failure class this card
exists to fix.

## Independent review (Opus, isolated worktree, different context from Dev)

Spawned as a fresh subagent per the `reviewer` skill (complexity:medium →
Sonnet builds, Opus reviews). It read the diff cold, ran the full gate
itself, and **independently redid the TDD revert-then-restore check**
rather than trusting the Dev/Tester claim:

- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./internal/discovery/...`, `guard-data-access.sh`, `guard-i18n.sh` — all
  clean.
- `go test ./internal/discovery/... -race -count=1 -v` — all tests pass,
  no race reports; also ran `-count=50 -race` on the sweep tests
  specifically to pressure-test the new concurrency path.
- `go test ./internal/pages/...` — green (the only caller of
  `sweepListeners`/`SweepPrinters`, via `kitchen_stations_page.go`).
- Reverted only `sweep.go` to `origin/main` (kept the test file at branch
  state) and re-ran `TestSweepPrinters_RetriesAMissOnceWithAColdARPCache`:
  it failed with a real assertion error — "got \[{...192.168.1.9:9100...}\],
  want both printers" — precisely #1608's symptom (the cold-cache host
  silently dropped, the warm one still found). Restored the fix; the
  suite passed again. Genuine TDD, not a tautology.
- Wrote three throwaway probes (deleted after) to verify by experiment,
  not just by reading: `skip` is honored during the retry pass even when
  a genuine miss forces that pass to run; a host that succeeds in pass 1
  is never re-dialled by the retry; cancelling the context mid-sweep
  aborts the retry pass the same way it aborts phase 1 (bounded dial
  count, not unbounded); the race detector agrees the `missed`/`listeners`
  slices are properly synchronized under `sync.Mutex`.
- Confirmed no UI/i18n/money/plugin-signing surface touched, no real
  client/shop name, no secret-shaped literal, and neither of the two
  recurring bug classes this pipeline has repeatedly found elsewhere
  (a file-write handler missing `os.MkdirAll`; a cwd-relative path where
  `paths.Data(...)` belongs) — this diff writes to no path at all.

## Findings — triaged and fixed in this same branch

1. **Real, non-blocking → fixed.** `kitchen_stations_page.go`'s
   `discoverPrintersTimeout` (12s) and its comment's latency math were
   invalidated by the retry (see "What shipped" above). Bumped to 20s,
   comment corrected with the new worst-case arithmetic.
2. **Real, non-blocking → fixed (comment accuracy).** `sweepListeners`'s
   own doc comment claimed the retry is "cheap" and targets "the misses"
   as if that were a small set — on a typical LAN, the addresses with no
   device at all time out identically on both the first pass and the
   retry, so `missed` is usually close to the *whole* subnet, not a
   handful. Corrected the comment to say so plainly (this was also the
   root cause of finding 1 slipping through — the misleading comment is
   what made the stale timeout look fine on a re-read).
3. **Test-coverage gap → fixed.** `TestSweepPrinters_DoesNotWriteToSkippedAddresses`
   had zero coverage of the retry path — every configured host answered
   on the first dial, so `missed` was always empty and the retry branch
   never executed; the "skip is respected" assertion was true only because
   the code path it's meant to guard never ran. Added a third host
   (`failDials: 1`) that genuinely misses its first dial, forcing the
   retry pass to run, while the skipped address is still asserted
   untouched.
4. **Nitpick → fixed.** The retry pass relied on an implicit invariant
   (nothing in `skip` ever reaches `missed`) rather than re-checking `skip`
   itself, on a security-scoped path whose own test's name promises a
   skipped address is "not touched at all". Added the explicit
   (currently-redundant) check, since `Listens` writes nothing either way
   and the cost is negligible.
5. **Nitpick → fixed.** No `ctx.Err()` short-circuit before starting the
   retry pass meant an already-cancelled context would still spawn one
   abandoned goroutine per miss (harmless, since `forEachBounded` bails
   immediately, but pure churn). Added the guard.
6. **Nitpick → fixed (comment).** The `failDials` test-helper field's
   comment didn't note that it counts *every* dial to an address,
   including `SpeaksESCPOS`'s own probe dial, not just the phase-1
   `Listens` dial — clarified, and the regression test's assertion
   tightened from a loose `n < 2` lower bound to the exact expected count
   (3: the miss, the retry, the ESC/POS probe).

Nothing was found that needed bouncing back to a fresh Dev pass — all six
items were small, local fixes applied and re-verified in this same
branch.

## Verified beyond automated tests

- Full repo gate re-run after every fix (not just the specific case each
  finding named): `gofmt -l .` (silent), `go build ./...`, `go vet ./...`,
  `go test ./internal/discovery/... -race -count=1` (green, no race
  reports), `go test ./internal/pages/...` (green — the only caller),
  `golangci-lint run ./...` (0 issues), `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh` (all green).
- This is not a UI/visual surface (no template, no page markup changed —
  `kitchen_stations_page.go`'s edit is a doc comment plus a constant),
  so no screenshot/visual-check attestation applies.

## Explicitly deferred / out of scope here

- **ut-docs#1608's own acceptance criterion 1** — "verified on real
  hardware, from a genuinely cold cache" — is out of reach for a cold
  cloud/cron session: there is no real LAN, no real ARP table, no real
  printer. The regression test validates the *decision logic* (a host
  that fails its first dial but answers a later one must still end up in
  `listeners`) and provably fails pre-fix for the right reason, but it
  cannot validate the physical premise (that phase 1's own traffic
  actually warms the ARP entry in time, and that the retry starts far
  enough behind the flood to win). Recording this distinction rather than
  claiming the criterion satisfied — this card is not moved to `Done`
  because of it; see the close-out comment on the issue for the exact
  hand-off.
- **Backlog candidate (not filed as blocking this PR):** the sweep now
  costs a full second near-full-subnet pass rather than a targeted retry
  over a genuinely small miss set, because `Listens`'s boolean result
  can't distinguish "no device here" from "device here, ARP was cold".
  A smarter version could narrow the retry set via `/proc/net/arp`
  (Linux-only) or a longer per-host timeout applied only to a
  demonstrably-live L2 neighbor — deferred as a separate improvement, not
  needed to close this card per the issue's own chosen option.
- **Backlog candidate (pre-existing, not introduced by this change):**
  `sweepListeners` returns a `nil` error on context cancellation
  (`sweep.go`), so a truncated scan is indistinguishable from one that
  legitimately found nothing — worth making observable in its own right,
  independent of this fix.

## Safe-to-merge verdict

**Yes.** Independent review found no blockers; every real finding was
fixed and re-verified in this branch; the full gate is green.
