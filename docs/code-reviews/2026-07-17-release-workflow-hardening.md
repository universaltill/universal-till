# Code review — release workflow hardening (outage resilience)

**Date:** 2026-07-17
**Branch:** `ci/release-outage-hardening`
**Trigger:** v0.2.9/v0.2.10 published with no `.dmg`/`.exe` during a GitHub
API outage: goreleaser got sustained 503s, retried, double-uploaded, every
asset 422'd (`already_exists`), the goreleaser JOB failed *after* the release
and archives were live, and `macos-app` (`needs: goreleaser`) plus the NSIS
step (same job, after the failed action) were skipped.

## What changed (`.github/workflows/release.yml`)

- **NSIS installer moved to its own `windows-installer` job.** It no longer
  shares the goreleaser job's workspace — it downloads the windows zip from
  the published release (`gh release download`), so it works on a re-run or
  after a partial goreleaser failure.
- **`windows-installer` and `macos-app` no longer require goreleaser to have
  succeeded:** `if: !cancelled() && needs.prepare.result == 'success'` with
  `needs: [prepare, goreleaser]` keeps the ordering but tolerates a failed
  (not cancelled) goreleaser.
- **Both jobs gate on the release actually existing**: a wait step polls
  `gh release view` (30 × 20s = 10 min) and fails with a clear message when
  goreleaser died *before* creating the release — they never create the
  release themselves, so goreleaser stays the single creator and a
  from-scratch failure still reads as "re-run goreleaser first".
- `macos-app`'s wait runs **before** the slow .app build.
- `RELEASING.md` updated to describe the decoupled jobs.

## Not changed

- goreleaser's own retry/idempotency: it has no native "tolerate 422
  already_exists" switch; resilience now comes from the add-on jobs not
  depending on its verdict. A goreleaser job that fails after publishing
  leaves a complete release (archives + installer + dmg) with only the
  workflow run showing red for the archive step.

## Verification

- YAML parses (ruby yaml).
- Logic exercised by the next release run (v0.2.11) — the normal path is
  unchanged apart from job boundaries; the failure path can't be usefully
  simulated locally.
