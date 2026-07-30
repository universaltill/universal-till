# Code review — macOS in-app updater: fail-closed .dmg checksum + Intel-Mac guard

- **Date:** 2026-07-30
- **Branch/PR:** `fix-macos-updater-checksum`
- **Author:** pipeline lane B (sonnet subagent, implement+test only)
- **Reviewer:** pipeline orchestrator (fable — different model from the author, per standing practice)
- **Scope:** `internal/selfupdate/macapp_darwin.go`, `internal/selfupdate/macapp_darwin_test.go`, `.github/workflows/release.yml`, `packaging/macos/update-checksums.sh` (new)

## What shipped

Closes the queue item found by coverage batch 2's independent review: the mac
update path trusted `codesign --verify` alone (internal signature consistency
only — a tampered ad-hoc-signed bundle passes), and hardcoded the arm64 asset
name so Intel Macs got a confusing "no macOS .dmg in release" error.

1. **Fail-closed SHA-256 gate**: `applyMacApp` now verifies the downloaded
   `.dmg` against the release's `checksums.txt` (existing `checksumFor` +
   `verifySHA256` helpers, same contract as the archive path from PR #90)
   between download and `hdiutil attach`. Missing `checksums.txt`, missing
   entry, or mismatch all abort via `cleanupOnErr` (bad download deleted).
2. **Release side**: the `.dmg` was *never in* `checksums.txt` — goreleaser
   only checksums its own artifacts and the dmg is built by the separate
   `macos-app` job. Without fixing that, the fail-closed gate would have
   bricked every mac update. New workflow step waits for goreleaser's
   `checksums.txt` (30×20s retry, mirroring the existing wait pattern),
   folds the dmg's SHA-256 in via the new idempotent
   `packaging/macos/update-checksums.sh` (replace-then-append, safe on
   re-runs), and re-uploads with `--clobber`.
3. **Intel guard**: `.goreleaser.yaml` builds darwin/arm64 only, so the right
   fix is a clear refusal — new `goarch` seam; non-arm64 returns a plain
   "not available for Intel Macs, download from the website" error before any
   network call.

## Review verification (beyond the author's own runs)

- **TDD arc re-verified personally**: stashing the production change makes the
  new tests fail (red confirmed); restored, all green. Additionally
  **mutation-probed the gate itself**: with `verifySHA256`'s abort bypassed,
  `TestApplyMacAppDmgChecksumMismatchAborts` fails with
  `err = mount .dmg…, want checksum mismatch` — the security test genuinely
  guards the gate, it isn't a tautology. (Process note: the restore after
  probing initially discarded the uncommitted fix; it was re-applied via
  `git apply` from the captured diff — diffstat byte-identical to the
  author's, and the full `-race` suite re-run green afterward.)
- `packaging/macos/update-checksums.sh` has its exec bit (invoked directly by
  the workflow — a non-executable file would fail the release job), `bash -n`
  clean; author additionally ran shellcheck + a manual append/replace/missing-
  artifact exercise.
- Full gate re-run by reviewer in the worktree: `go build ./...`, `go vet`,
  `go test ./internal/selfupdate/ -race`, both CI guards, workflow YAML
  parse — all green. Author also ran the full-repo `-race` suite green.
- **i18n decision accepted**: the new error strings match this exact path's
  existing precedent — `update_api.go` returns `err.Error()` in JSON/logs
  only; the UI button shows a localized generic failure. No template-visible
  string added, so no locale keys needed (guard-i18n green confirms).
- No file writes outside the existing `cleanupOnErr`-managed temp dir; no
  cwd-relative paths introduced; no real shop names; no secrets.

## Honest limits

- The workflow half can only be fully proven on the next real `v*` release
  run — DevOps must watch the `macos-app` job's new step and confirm the
  published `checksums.txt` gains the dmg line. Until a release built with
  this workflow exists, a new binary's mac in-app update would fail closed
  (correct direction; release N ships both halves together, so the first
  update *from* N targets a release that has the entry).
- `hdiutil attach`/`ditto`/`codesign`/detached-updater glue remains
  deliberately untested (real macOS side effects), unchanged by this fix.

## Deferred (logged to ut-docs/QUEUE.md)

- Intel Macs still *see* the Update button (`Supported()` returns true) and
  now get a clean refusal; the nicer Windows-style treatment (hide button,
  show a download-page link) is a UX follow-up.
- `verify-versions` release job could additionally assert the dmg's
  `checksums.txt` entry matches the published artifact ([[test-everything]]
  regression gate for the new workflow step).

## Verdict

**SAFE TO MERGE.** Both halves ship together; fail-closed semantics match the
archive path; tests are mutation-verified real.
