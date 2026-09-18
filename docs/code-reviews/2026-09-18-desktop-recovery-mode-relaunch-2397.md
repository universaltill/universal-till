# Desktop shell: recovery-mode relaunch spawns a second server (ut-docs#2397)

**Date:** 2026-09-18
**Card:** universaltill/ut-docs#2397
**PR:** universaltill/universal-till#(pending, see below)
**Review:** independent, fresh-context Sonnet subagent, isolated worktree — never saw the implementation reasoning.

## What shipped

`cmd/unitill-desktop/desktop.go`'s `tillAlreadyRunning(addr)` decides whether
a relaunch of the desktop shell attaches to an existing `unitill-pos` server
or spawns a new one. It previously counted only a `/healthz` **200** as
"already running". Boot-failure recovery mode (ADR-0075) answers `/healthz`
with **503** for as long as it's serving, so relaunching while an existing
server sat in recovery mode did not attach to it — it spawned a second
`unitill-pos` against the same locked data directory, which hit
`db.ErrDataDirLocked` and hard-exited, leaving the shell showing nothing
useful.

The fix mirrors the already-shipped precedent in `mobile/mobile.go`'s
`waitUntilReady` (ut-docs#1437): a 503 carrying header
`X-UT-Mode: recovery` (`internal/recovery.HeaderMode` /
`internal/recovery.ModeRecovery`) now also counts as "running"; a bare 503
(a foreign process on the port, or any other unhealthy state) still does
not.

New test file `cmd/unitill-desktop/tillrunning_test.go` (build-tagged
`desktop`, same as `desktop.go` itself) covers all three cases against an
`httptest.Server`, exercising the real production `tillAlreadyRunning`
function directly (not a reimplementation).

## Independent review

Fresh-context Sonnet subagent, isolated worktree, no visibility into the
implementation reasoning. **Verdict: PASS — no blocker, no should-fix
findings.**

Verified directly, not taken on faith:
- `internal/recovery.HeaderMode`/`ModeRecovery` values and where
  `healthHandler` actually sets them (`internal/recovery/page.go`) match
  exactly what the fix checks.
- **TDD claim re-verified personally**: reverted `tillAlreadyRunning` to the
  old 200-only logic, re-ran the three new tests — the recovery-mode test
  fails with the exact pre-fix symptom (`tillAlreadyRunning() = false, want
  true`), while the bare-503 and healthy-200 tests (regression guards for
  unchanged behavior) still pass, as expected. Restored the fix, re-ran —
  all three pass again, file byte-identical to the pre-revert version.
- Full gate: `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `go test ./...` (no tags), `CGO_ENABLED=1 go build/vet -tags desktop
  ./cmd/unitill-desktop`, `golangci-lint run --config=.golangci-desktop.yml
  --build-tags=desktop ./cmd/unitill-desktop/...` (v2.5.0) — all clean, 0
  lint issues.
- Scope: diff is exactly the two files the ticket describes, no unrelated
  changes swept in.
- False-positive risk (some unrelated 503 handler on the same port
  coincidentally carrying the same header) assessed as theoretical, not
  real: the header is this codebase's own internal recovery-mode signal,
  set only by `internal/recovery`'s `healthHandler`, and the probe address
  is loopback-only.
- `CLAUDE.md` non-negotiables (repository-pattern SQL, i18n, money type) —
  confirmed not applicable (no SQL, no user-facing strings, no amounts
  touched).

`TestDesktopWindowOps_ExitToOSRecordsAppliedMode`'s known, pre-existing,
unrelated SIGSEGV under `-tags desktop` in a no-display sandbox (why this
repo's own `desktop-shell` CI job runs build+vet only, never `go test`, for
this package) was not encountered — every test run was scoped with `-run
'TestTillAlreadyRunning'` specifically to avoid it, per the review brief.

## Test plan

- [x] `gofmt -l .` — clean
- [x] `go build ./...`, `go vet ./...` — clean (plain build correctly falls
      back to `stub.go` under `!desktop`)
- [x] `go test ./...` (full suite, no build tags) — all packages green
- [x] `CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop` — clean
- [x] `CGO_ENABLED=1 go vet -tags desktop ./cmd/unitill-desktop` — clean
- [x] `CGO_ENABLED=1 go test -tags desktop -run 'TestTillAlreadyRunning' -v
      ./cmd/unitill-desktop/...` — 3/3 pass
- [x] `CGO_ENABLED=1 golangci-lint run --config=.golangci-desktop.yml
      --build-tags=desktop ./cmd/unitill-desktop/...` — 0 issues
- [x] `bash scripts/ci/guard-deadcode-baseline.sh` (desktop tag) — clean
- [x] TDD re-verified twice independently (implementer + reviewer):
      reverting the fix makes the recovery-mode test fail with the original
      symptom; restoring it makes all three pass again
- [x] Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job
      run locally — all green

## Not driven live

No full desktop-shell relaunch was driven against a real display (Xvfb was
available but not used) — the acceptance criteria concerns
`tillAlreadyRunning`'s decision logic, not window rendering, and no
GUI/window code was touched. The unit tests exercise the actual production
decision function against a real HTTP server, matching the rigor of the
existing `mobile.waitUntilReady` precedent this fix mirrors.
