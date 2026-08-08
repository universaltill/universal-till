# Code review: root-aware skip for TestSaveCleansUpDirectoryOnWriteFailure

**Card:** universaltill/ut-docs#258 (duplicate universaltill/ut-docs#415 closed against it)
**Complexity:** easy
**Reviewer:** fresh-context Sonnet subagent (independent of the implementing session)

## Change

One test-only addition to `internal/issuereport/bundle_test.go`:

```go
if os.Geteuid() == 0 {
	t.Skip("running as root; read-only dir does not block writes")
}
```

Added immediately after the existing Windows skip inside
`TestSaveCleansUpDirectoryOnWriteFailure`, mirroring the identical
pattern already established in
`internal/selfupdate/selfupdate_apply_test.go:417`. No product code
(`internal/issuereport/bundle.go`) was touched.

## Why

The test pre-creates a bundle directory at mode `0o500` and expects
`Save`'s write into it to fail. A process running as root (uid 0)
bypasses directory permission bits (`CAP_DAC_OVERRIDE`), so the write
silently succeeds and the test's own precondition never holds — a false
failure, not a product defect. Confirmed to reproduce identically on
unmodified `main` in this container (which runs as uid 0).

## Verification

Performed independently by both the implementing session and the
reviewing subagent (each ran these directly, not by trusting the other's
report):

- Reproduced the original failure as root on unmodified `main`:
  `expected Save to fail on a read-only bundle directory`.
- After the fix: `go test ./internal/issuereport/... -v` shows
  `TestSaveCleansUpDirectoryOnWriteFailure` SKIPping cleanly with the
  expected message, as root.
- Ran the same test as a genuine non-root user (copied the repo, chowned
  to a non-root account) — the test still **passes for real** (not
  skipped), proving the fix only excuses the uid-0 case where the
  assertion is fundamentally unobservable; it does not weaken coverage
  for the case the test actually exists to catch.
- `go build ./...` — clean.
- `go test ./...` — full suite green, as root.
- `bash scripts/ci/guard-data-access.sh` — pass (no SQL touched).
- `bash scripts/ci/guard-i18n.sh` — pass (no user-facing strings touched).
- Reviewer independently repeated the revert → confirm-fails → restore
  cycle (stash the fix, rerun, confirm the original failure message,
  pop the stash) to verify the TDD claim personally rather than take it
  on faith.

## Audit: any other test with the same fragility?

Per the issue's own acceptance criterion ("audit for any other test
relying on permission-bit enforcement without the guard"), swept the
whole repo for:

- `os.Geteuid` — 5 existing guards found (selfupdate:417,
  issuereport:208 [new], paths:258/362/431).
- Restrictive-mode `Chmod`/`MkdirAll` calls (`0o000`, `0o500`, `0o555`,
  `0o444`, `0o400`, `0o200`) — 5 call sites found (selfupdate:427,
  issuereport:220, paths:261/366/435), each already covered by a
  `Geteuid` guard — a clean one-to-one match.
- `os.Chown`, `chattr`, `umask` — none found anywhere in the test suite.

No other test in the repo carries this fragility. This audit criterion
is satisfied by inspection, not by assumption.

## Contract check

`Save`'s behavior, error wrapping, and cleanup-on-failure logic are
unchanged — verified by reading `bundle.go` directly. This is a pure
test-observability fix: root could never validly exercise this
assertion, and now the test says so explicitly instead of failing for
the wrong reason.

## Outcome

No findings. Safe to merge as-is.
