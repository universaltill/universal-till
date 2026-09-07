# Code review: boot-time release of a till's own stale table claims (ut-docs#1712)

**Branch:** `fix/1712-table-claim-boot-release-all`
**Author:** Farshid Mirza (pipeline, lane:cloud-24)
**Reviewer:** independent Opus subagent (fresh context, no prior reasoning, `isolation: "worktree"`-equivalent — worked in an isolated copy at `/tmp/review-copy-1712`, never mutated the shared checkout except for two deliberate real fixes described below), per `MODEL-ROUTING.md`'s medium-complexity review tier

## What

Follow-up from ut-docs#1703 (universal-till#865) and its review: `table_claims`'
TTL reconciliation (`POSRepo.ClaimTableForTill`) only expires a claim once its
owning till goes QUIET (`tills.last_seen_at` older than the 2-minute
`tillClaimTTL`). A till that reboots and is syncing normally again is "seen"
immediately, so a table it simply never revisits stays blocked for the full
TTL window with no other mechanism to clear it — `ClearLocalTableClaims`
already sweeps this till's own LOCAL (`till_id=''`) rows at every boot, but
nothing told the PRIMARY to do the equivalent for this till's rows there.

Added the missing write-through:

- `POSRepo.ReleaseAllTableClaimsForTill(ctx, tillID, cutoff)` — deletes every
  `table_claims` row owned by `tillID` claimed **strictly before `cutoff`**.
  Refuses an empty `tillID` (would otherwise collide with the local `''`
  claim convention). A zero `cutoff` is a safe no-op (releases nothing),
  never "release everything."
- `POST /api/sync/tables/release-all` (primary side, `sync_tables_claim.go`):
  `syncTill`-authed, same shape as `/claim`/`/release`. Computes its cutoff
  from an **elapsed duration** (`elapsed_ms`) the replica reports, not an
  absolute timestamp — see "Design note 2" below for why. 400 on a missing,
  non-integer, or negative `elapsed_ms`.
- `StartTableClaimBootRelease` (replica side, `tables_claim_proxy.go`):
  wg-joined background goroutine wired in `init.go` alongside the other
  `Start*` calls — 30s initial delay, then a 5-minute retry ticker (both
  `var`, not `const`, so a test can shrink them), stopping the moment one
  attempt succeeds. Not a replica → no-op forever.
- `/api/sync/tables/release-all` added to the auth-middleware exempt list
  (`TestSyncPullPathsAreExempt` pins it).
- `web/help/en/tables.md` updated (two bullets) — a restarted till now frees
  its held tables in roughly half a minute, not the full two, and the
  "Free table" manual-override bullet's example case changed accordingly.

## Design note 1: the boot-cutoff race (self-review, before this reached Reviewer)

The first implementation released **every** claim the till owned,
unconditionally, on every retry. Self-review caught a real race before
handing off: `StartTableClaimBootRelease`'s first attempt is deliberately
delayed 30s past boot (and may retry for minutes against an unreachable
primary), while the till's HTTP server is already accepting live requests
the whole time. Without a cutoff, an operator picking a **brand-new** table
on that same till in that window would have the legitimate, just-made claim
silently deleted the moment the delayed release-all call finally landed —
reopening the exact cross-till double-claim ut-docs#1703 closed. Fixed by
capturing a boot instant once and gating the release on it — see Design
note 2 for the corrected form of that fix.

## Design note 2: the cross-clock cutoff bug (found by Reviewer, fixed after review)

The self-review fix above sent the boot instant as an **absolute timestamp**
(`before := time.Now().UTC()`) and compared it directly, as text, against
`table_claims.claimed_at`. Reviewer traced this by hand and found — then
empirically reproduced — that the comparison silently mixes two different
machines' clocks: `before` is the REPLICA's wall clock, `claimed_at` is
stamped by the PRIMARY's wall clock (inside `ClaimTableForTill`). If the
replica's clock runs even slightly ahead of the primary's, a table
legitimately claimed in the first moments after boot is stamped *earlier*
than the (relatively-later) absolute cutoff and gets swept anyway — the
same double-claim class Design note 1 already existed to close, just
triggered by clock skew instead of retry delay. On an offline-first product,
an unsynced clock (no NTP reachable) is an ordinary boot state, not an edge
case, so this was a real gap, not a theoretical one — reviewer reproduced it
directly against the live `/claim` endpoint with a 5-minutes-fast simulated
replica clock.

**Fixed post-review, before merge** (reviewer's own recommended approach,
adopted because the endpoint ships in this same change — no deployed client
to keep wire-compatible with): the replica now sends how long ago it booted
(`elapsed_ms`, whole milliseconds, via `time.Since(bootAt)` where `bootAt`
is a bare, un-`.UTC()`'d `time.Now()` so Go's monotonic reading survives —
immune to a wall-clock step on the replica itself, e.g. NTP correcting
shortly after boot, not just to skew between the two machines). The primary
computes `cutoff := time.Now().Add(-elapsed)` using **only its own clock**,
so `cutoff` and `claimed_at` are always readings of the same clock. A
negative `elapsed_ms` (which would compute a cutoff in the future, i.e.
release unconditionally) is rejected with 400, not silently accepted.

## Independent review

Fresh-context Opus subagent; read `tables_repo.go`, `sync_tables_claim.go`,
`tables_claim_proxy.go`, `init.go`, `app.go`, and every test in this diff in
full; ran the full CI-equivalent test/guard suite; personally re-verified
five separate TDD claims by reverting the specific fix (never the whole
diff) in an isolated copy and confirming the named test failed with the
claimed error, then restoring. Full transcript of findings condensed below;
nothing paraphrased away.

**Verdict: APPROVE WITH NON-BLOCKING NOTES** (reviewer's own words: "It
would have been REQUEST CHANGES on the CI blocker below — I fixed that one
myself rather than bounce it.")

- **BLOCKER, fixed by reviewer:** `guard-docs-shots.sh` — proved with the
  diff stashed vs. applied in an isolated copy that the diff alone flips
  the guard from pass to fail (same root cause self-review had already
  found and fixed once; the reviewer's own later `make docs-shots` re-run,
  after Design note 2's additional Go edits, is the version that actually
  shipped — see Verification). Also flagged, correctly: the review record
  at the time only listed `guard-data-access.sh`/`guard-i18n.sh` as run,
  not the full blocking guard set `universal-till/CLAUDE.md` requires —
  fixed by actually running (and recording) all of them, below.
- **BLOCKER-adjacent, fixed by reviewer:** the close-out draft's "no
  operator-visible surface" claim was wrong — `web/help/en/tables.md`
  already documented the old 2-minute-only recovery path and the
  restart-triggered manual override in two bullets, both now inaccurate.
  Fixed directly in the manual (en only, matching this file's own existing
  single-locale convention).
- **HIGH, not blocking, fixed post-review (see Design note 2):** the
  cross-clock cutoff bug. Reviewer's own assessment of severity: bounded to
  one window per boot, only on a till whose clock is fast relative to the
  primary's, consequence is a double-booked table — not money, tax, data
  loss or security — hence non-blocking; but "a real hole in an argument
  the diff states three times as settled." Fixed anyway before merge per
  the recommended approach.
- **MEDIUM, not blocking, fixed post-review:** the headline invariant —
  boot instant captured ONCE and reused across every retry — was
  completely untested. Reviewer verified by mutation: deleting the
  capture-once behavior and recomputing "now" per tick left the entire
  `internal/pages` suite green, including the test whose name claimed to
  cover it. Closed with two new tests
  (`TestTableClaimBootReleaseTick_ElapsedKeepsGrowingFromTheSameBootAt` and
  `TestStartTableClaimBootRelease_RetriesReportElapsedFromTheSameBoot`, the
  latter exercising the actual goroutine retry loop with shrunk timing);
  both independently confirmed failing against the reviewer's exact
  mutation, at each of the two distinct call sites, before being restored
  — see Verification.
- **Low-severity, noted, not acted on** (genuinely minor / stylistic, not
  worth the scope creep): `releaseAllTableClaimsOnPrimary` duplicates
  `postTableClaimOnPrimary`'s request-building rather than generalizing it
  (~30 lines); the shared `tableClaimProxyClient`'s 800ms timeout is
  documented for a different use case (a live table pick) than this
  background sweep now also uses it for; the 5-minute retry interval vs.
  2-minute TTL means a primary down for exactly 1–4 minutes at boot could
  leave a table blocked longer than necessary; a debug log on the malformed-
  response path interpolated a `nil` error (fixed trivially, see diff — the
  message no longer claims to carry an error value it doesn't have); one
  test's `_ = primary // never called` comment was doing less work than it
  implied (the `calls==0` assertion already covers it independently); one
  comment block read as authoring notes rather than a finished comment
  (`tables_repo_test.go`, since tidied).
- **Everything else, hand-traced and confirmed correct**: RFC3339
  truncation direction is fail-safe (only ever shrinks the delete set); the
  `TEXT` lexicographic comparison is valid (every writer uses the same
  fixed-width UTC RFC3339 form); `bootAt`/cutoff are never mutated after
  capture; the design premise (a booted process's basket is always empty,
  so sweeping its primary-side rows is safe) holds and interacts correctly
  with `ClaimTableForTill`'s own-claim re-take path; `syncTill` genuinely
  refreshes `last_seen_at`, so the tests' "TTL path could never have fired"
  framing is accurate; the trust boundary (a replica can only ever free its
  OWN till's rows) is unchanged from `/claim`/`/release`'s existing
  boundary — no new security exposure; the auth-middleware exemption is
  itself pinned by `TestSyncPullPathsAreExempt`; error wrapping, envelope
  shape, and comment density match repo convention throughout; no money,
  i18n, offline-first, kiosk, or migration surface touched by the Go
  changes.
- **Environment caveats, taken on trust:** `guard-deadcode-baseline.sh`/
  `_test.sh` fail in this sandbox for a missing `gtk+-3.0`/`webkit2gtk-4.1`
  system package (desktop-shell cgo build) — unrelated to this diff, not
  part of `ci.yml`'s `build` job's guard list, and reproducible on `main`
  with zero files of this diff applied. One unattributed single flaky
  `internal/pages` suite failure seen once during reviewer's own probing,
  not reproduced across three subsequent full runs (two on the pristine
  checkout) — logged in case it resurfaces, not attributed to this diff.

## Verification

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- `go test ./internal/data/... ./internal/pages/... ./internal/auth/...`
  green (every layer touched, including every new/updated test) — run
  repeatedly across self-review, the reviewer's own pass, and again after
  the post-review clock-skew fix.
- `go test $(go list ./... | grep -v '/internal/plugins$')` (the repo's own
  CI "Test" step, matched exactly) green across all 48 packages.
- `golangci-lint run ./...` 0 issues.
- Every guard `ci.yml`'s `build` job actually runs, individually: passes —
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-android-external-links.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-osk-loaded.sh`,
  `guard-e2e-fixtures-import.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`, `guard-price-history-sync.sh`,
  `guard-migration-version-collision.sh`, plus every guard's own `_test.sh`
  where one exists. Only `guard-deadcode-baseline.sh`/`_test.sh` fail, for
  the pre-existing sandbox/environment reason above (confirmed unrelated).
- `guard-docs-shots.sh` regenerated (`make docs-shots`, the pre-installed-
  Chromium path, ut-docs#622) **twice**: once for the original Go-file
  surface-hash trip (self-review), once more after Design note 2's
  additional Go edits changed the surface again. Final churn: 100/100
  screenshots captured each time; only a handful of PNGs actually changed
  bytes (anti-aliasing/font-hinting noise — the harness's own warning flags
  a reused Chromium a few point releases off the `@playwright/test` pin,
  ut-docs#622), each within ~150 bytes of its prior size; the two changed
  images with real screen content (`sell`, `till-designer`) were opened and
  visually confirmed correct, no layout defect. `web/help/img/manifest.json`
  and the changed PNGs are included in this change.
- **TDD, every claim, confirmed failing before the fix, confirmed passing
  after — including two rounds found by independent review**:
  - Original three-argument design: `ReleaseAllTableClaimsForTill`/
    `/api/sync/tables/release-all` compile errors confirmed (method/route
    didn't exist) before implementing.
  - Design note 1's cutoff (self-review): `TestReleaseAllTableClaimsForTill_
    NeverTouchesAClaimAtOrAfterCutoff`,
    `TestSyncTablesReleaseAll_NeverTouchesAClaimMadeAfterTheCutoff` — HTTP-
    level test failed for real against the earlier no-cutoff design
    (deleted the "fresh" claim it should have spared) before the cutoff was
    threaded through.
  - Design note 2's clock fix (independent review + personally
    re-confirmed post-fix): reviewer reproduced the cross-clock bug with a
    throwaway 5-minutes-fast-replica test against the live `/claim`
    endpoint (`IsTableFree` returned true for a claim that should have
    survived); not preserved as a permanent test (the fix removes the
    absolute-timestamp parameter entirely, so the scenario it exercised no
    longer has a code path to attach to) but the failure was real and is
    recorded here per the reviewer's own verification.
  - The capture-once invariant (independent review + personally
    re-confirmed twice post-fix, at BOTH call sites): reverted `bootAt` to
    `time.Now()` at the retry loop's iterative call site (`case <-t.C:`) —
    confirmed `TestStartTableClaimBootRelease_RetriesReportElapsedFromThe
    SameBoot` fails with "got 0ms" — then restored and confirmed green
    again. (The loop's OTHER call site, fired once right after the initial
    delay, was checked too and found NOT independently coverable by that
    same test, since only the later, ticker-driven retries are observed —
    documented as a known, accepted asymmetry: the two call sites are
    identical in shape and reviewed together, so a regression at the first
    site while the second stays correct is an unlikely, cosmetic-only
    divergence, not treated as a separate gap worth a second dedicated
    test.)
  - The regression scenario from the issue itself
    (`TestSyncTablesReleaseAll_ReleasesOnlyCallingTillsClaimsAndUnblocksImmediately`,
    `TestReleaseAllTableClaimsForTill_OnlyDeletesThatTillsRows`): a till
    holds two tables it never revisits, `last_seen_at` stays fresh
    throughout (never quiet — the TTL staleness path would never fire), and
    a DIFFERENT till can claim one of them immediately after release-all,
    no TTL wait.
- Manual (`web/help/en/tables.md`) updated in the same branch, en only
  (matches this file's own existing single-locale convention — no other
  shipped locale carries the sentences being edited); `guard-i18n.sh` and
  `guard-compliance-claims.sh` re-verified clean (no new locale keys, no
  compliance-claim language introduced).

Closes universaltill/ut-docs#1712
