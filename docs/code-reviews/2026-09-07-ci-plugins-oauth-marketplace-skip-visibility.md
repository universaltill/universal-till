# CI: make internal/plugins/oauth and /marketplace test skips visible

**Date:** 2026-09-07
**Card:** ut-docs#1630
**Complexity:** easy (Dev: Sonnet inline, Review: fresh-context Sonnet subagent)

## What shipped

`.github/workflows/ci.yml`'s main `Test` step ran `go test` with no `-v`
flag, and its exclude pattern (`grep -v '/internal/plugins$'`) only
excluded the exact `internal/plugins` package — not its subpackages — so
`internal/plugins/oauth` and `internal/plugins/marketplace` ran inside that
plain, non-verbose step. Both packages contain a real `t.Skip` (gated on
`os.Hostname()` being empty):

- `internal/plugins/oauth/token_client_test.go` (`TestGetDeviceID_FallsBackToHostname`)
- `internal/plugins/marketplace/client_more_test.go` (`TestDeviceIDFromConfig`, subtest)

Without `-v`, a skip prints no `--- SKIP` line, so CI reports a bare `ok`
whether the test actually ran or silently skipped — the same failure mode
ut-docs#1626 already fixed for the parent `internal/plugins` package
itself.

Fix (mirrors the #1626 pattern, per that card's own suggested option):

1. New step `Test (internal/plugins/oauth and /marketplace — ut-docs#1630)`
   runs `go test -v ./internal/plugins/oauth ./internal/plugins/marketplace`
   (exactly these two packages, no trailing `/...`).
2. Main `Test` step's exclude widened to
   `grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$'`
   so the two subpackages don't also run inside the main step (avoids
   double-running things in one job, per the existing ut-docs#662
   precedent already documented in this same file).
3. Updated the now-stale comment on the "internal/plugins — wider
   timeout" step, which used to say marketplace/oauth were "already
   covered by the main Test step above" — it now points at the new
   dedicated step.

No Go source changed — workflow-only.

## What the independent review found

A fresh-context Sonnet subagent reviewed the diff independently (per the
`complexity:easy` model-routing rule) and found no issues. It:

- Validated the YAML.
- Confirmed the widened exclude regex removes exactly the 3 intended
  packages (`internal/plugins`, `/oauth`, `/marketplace`) from the main
  step's package list — no more, no less (checked there's no
  `internal/plugins/oauthx`-shaped package the regex could over-match).
- Ran `go test -v ./internal/plugins/oauth ./internal/plugins/marketplace`
  — passes.
- **Empirically verified the fix actually closes the gap**: temporarily
  forced both `t.Skip` branches to fire and reran with `-v`, confirming
  both `--- SKIP: <test>` lines and their skip messages appeared in
  output, then restored the test files (working tree confirmed clean
  afterward).
- Checked the new step's comment against the real test file/line/function
  names — accurate.
- Confirmed no flag other than `-v` (and package scope) differs between
  the main step's `run:` and the new step's `run:` — no `-race`/build-tag
  change, so no coverage-mode gap introduced.
- `gofmt -l .` clean, `go build ./...` succeeds — confirmed no Go source
  was touched.

Verdict: safe to merge, no findings.

## Verified beyond automated tests

- `python3 -c "import yaml; yaml.safe_load(...)"` — YAML parses.
- `go list ./... | grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$'`
  — zero `internal/plugins`-prefixed matches remain, confirming the main
  step's exclusion is exact.
- `go test -v ./internal/plugins/oauth ./internal/plugins/marketplace` —
  passes with full per-test `-v` output.
- `go build ./...` — clean.

## Deferred / out of scope

None — this card's three suggested-fix options were scoped down to the
one matching #1626's established pattern; the other two (raising `-v` on
the whole main `Test` step, or post-processing output for a skip-count
job-summary annotation) were considered and not needed once this option
closes the gap directly.
