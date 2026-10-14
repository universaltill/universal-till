# Review — remove `pos.DOBBeforeCutoff` (ut-docs#4017)

**Date:** 2026-10-13 · **Lane:** `lane:cloud-41` · **Card:** universaltill/ut-docs#4017
**Author model:** Sonnet (Dev) · **Reviewer model:** Opus 5.5, fresh context

## What shipped

Option (b) from the card (groomed Ready with that default): remove the
self-baselined dead function instead of keeping it as the one grandfathered
exception to the shrink-only deadcode baseline (ut-docs#1566, #3406).

- `internal/pos/age_restriction.go`: `DOBBeforeCutoff` and its doc comment
  removed; the now-unused `time` import dropped.
- `internal/pos/age_restriction_test.go`: `TestDOBBeforeCutoff` removed;
  imports collapse to `import "testing"`.
- `scripts/ci/deadcode-baseline.txt`: its entry removed (baseline shrinks by 1).

It had no production caller (only its own test) since universal-till#1586
(ut-docs#3340). A future DOB-entry feature or plugin answer re-adds it with
its first real caller.

## Findings

None. Checked:
- No other reference in universal-till (Go, templates, JS, `web/help`,
  plugins, e2e) or in ut-docs reference docs claiming the code has the check
  (`reference/uk-compliance.md` describes the law/policy only).
- `guard-deadcode-baseline.sh`: a stale baseline line only prints a
  "burned down" notice, so removing it is cleanup, not required; deleting
  the line while keeping the function *would* fail (new unreachable func).
- Remaining header comment of `age_restriction.go` still reads correctly.

## Verified

`gofmt -l internal/pos` (empty), `go build ./...`, `go vet ./internal/pos/`,
`go test ./internal/pos/`, `golangci-lint run ./internal/pos/...` (0 issues),
`guard-deadcode-baseline-growth.sh` (no new entries vs `origin/main`).

`guard-deadcode-baseline.sh` cannot run fully in the cloud container (no
GTK/WebKit headers, so `./cmd/unitill-desktop` is skipped); the run that
works (`GOTOOLCHAIN=go1.27.1`) flags two `internal/logging/file.go` entries
whose only callers are under the skipped desktop root, identically with and
without this change, and no longer flags `DOBBeforeCutoff`. CI's full
three-root run is the authority.

No UI, locale, migration or behaviour change; no help topic affected.

## Verdict

Safe to merge.
