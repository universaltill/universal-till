# Code review — coverage batch 2: internal/selfupdate + internal/updates

- **Date:** 2026-07-30
- **Branch/PR:** `test/coverage-selfupdate-updates`
- **Author:** SDLC pipeline (fable) · **Independent reviewer:** different model (opus), subagent
- **Scope:** hermetic test coverage for the till's self-update mechanics —
  `internal/updates` (background release check) 29.6% → **82.5%**,
  `internal/selfupdate` (archive-swap updater) 5.0% → **76.3%**
  (darwin-measured; Linux CI doesn't compile the 117-line
  `macapp_darwin.go`, so its number lands higher there). Plus two real
  bug fixes found TDD-first, and behavior-preserving test seams.

## What shipped

- **Seams (production behavior unchanged; vars only ever mutated by tests
  with `t.Cleanup` restores; no `t.Parallel` anywhere in either package):**
  `releasesURL`/`releasesLatest` const→var (tests point them at local
  `httptest` servers); `osExecutable`, `reexecFn`, `reexecDelay`,
  `extractLimit` vars in `selfupdate`; `Start`'s env gate extracted to
  `enabledFromEnv()` (logic reproduced verbatim).
- **Tests:** full hermetic `Apply()` arcs against a fake release server on
  a temp-dir install fixture — successful binary+`web/` swap with `.bak`
  backups and stubbed re-exec; unsupported-install; already-latest;
  no-archive-for-platform; download 404; checksum mismatch (binary
  untouched, no premature `.bak`); archive-missing-binary; backup-rename
  failure (read-only dir). Unit tests for `download`, `checksumFor`,
  `verifySHA256`, `extractTarGz` (incl. `../` traversal rejection and
  oversize rejection), `moveFile`/`moveDir` error paths, `fetchLatest`
  branches. `updates`: `checkOnce` state machine (success stores
  status with `v`-trim; 403/bad-JSON/empty-tag/unreachable leave prior
  state), `CheckNow`, `enabledFromEnv` table, `Start` disabled/cancelled.
  Darwin-only: `applyMacApp`'s two early hermetic branches + `Apply`'s
  `.app`-bundle routing.

## Real bugs found (TDD: proven red first, then fixed, then green)

1. **Medium (security hardening): `Apply` silently skipped checksum
   verification when the release had no `checksums.txt` asset** — a
   fail-open on the self-update integrity gate.
   `TestApplyRefusesReleaseWithoutChecksums` proved red (`err = <nil>`,
   unverified archive installed) against the old code. Every real release
   ships `checksums.txt` (`.goreleaser.yaml` `checksum:` block), so a
   missing one only ever means a broken/tampered release; now fails
   closed. Reviewer independently confirmed no legitimate path breaks.
2. **Medium: `extractTarGz` silently truncated any archive entry at the
   512MB cap** (`io.LimitReader` + `io.Copy` cannot distinguish EOF from
   cap) — a truncated binary would then be swapped in, since the SHA-256
   covers the `.tar.gz`, not extracted files. Proven red with a small
   `extractLimit`: 64-byte entry extracted as 16 bytes, no error. Fixed
   by reading `limit+1` and rejecting `n > limit`; reviewer verified the
   boundary (an entry of exactly `extractLimit` bytes still extracts).

## Hermeticity (hard requirement for this batch)

- All tests pass with `HTTP_PROXY`/`HTTPS_PROXY` pointed at a dead port
  (loopback bypasses the proxy; any real GitHub call would die) — run by
  both tester and reviewer independently. No non-localhost URL literal in
  any test file.
- No test ever touches the real test binary: every path reaching the swap
  code stubs `osExecutable` to a `t.TempDir()` fixture; `reexecFn` is
  stubbed (the real one is `syscall.Exec` — reviewer confirmed stubbing
  is load-bearing, a real call would replace the test process).
- The macOS updater helper (`pkill`s real till processes, replaces
  `/Applications` bundles) never executes: only `applyMacApp`'s
  pre-`hdiutil` branches are tested.

## Independent review (different model, opus): SAFE TO MERGE, no blocking findings

- Re-proved both TDD claims itself by reverting each fix in isolation and
  reproducing the exact claimed failure modes, then restoring.
- Ran 2 fresh mutation probes of its own (inverted `fetchLatest`'s
  already-latest comparison; dropped `checksumFor`'s asset-name match) —
  both caught. On top of the tester's 3 probes (tar traversal guard
  removal, `checkOnce` empty-tag guard removal, `enabledFromEnv` gate
  removal) and the 2 TDD arcs: **7/7 caught, zero false-passes**.
- Verified seam behavior-preservation, cleanup restores, absence of
  `t.Parallel`, no real shop names, no secret-shaped literals.
- Non-blocking, out-of-scope observations → logged to `ut-docs/QUEUE.md`:
  the mac `.dmg` update path performs **no SHA-256 check** (relies solely
  on `codesign --verify`, which only proves internal consistency — an
  ad-hoc-signed tampered bundle would pass), and the `.dmg` asset name
  hardcodes `-arm64` (amd64 Macs get "no macOS .dmg"). Both pre-existing.

## Verified beyond automated tests

- Race detector clean on both packages (`go test -race`).
- Cross-compile checks: `GOOS=linux` and `GOOS=windows` build clean.
- Full repo gate: `go build ./...`, `go vet ./...`, full `go test ./...`,
  all 5 CI guards green.
- Playwright e2e deliberately not run: the diff touches zero
  UI/template/handler surface, and no e2e flow triggers (or ever should
  trigger) a real self-update.

## Honestly-untestable remainder (documented, not faked)

- `reexec_unix.go` — `syscall.Exec` replaces the process image; by
  definition cannot be exercised inside a test process.
- `macapp_darwin.go` past the `.dmg` download — `hdiutil` mount, `ditto`,
  `codesign`, and the detached replace-and-relaunch helper are real macOS
  glue; the helper kills real till processes and must never run in tests.
- `updates.Start`'s timer loop body — 30s boot wait / 24h ticker need a
  fake clock; restructuring for one was an explicit non-goal.
- `moveFile`/`moveDir` cross-filesystem copy success path — needs a
  second filesystem; error fallbacks are covered.
- Scattered local-IO error branches on just-created temp files
  (`os.MkdirTemp`/`os.Create` failures mid-`Apply`), and the mid-swap
  `web/` rollback branch, which requires a rename that fails only after
  the binary swap succeeded — not arrangeable without invasive FS mocking
  disproportionate to the risk.
