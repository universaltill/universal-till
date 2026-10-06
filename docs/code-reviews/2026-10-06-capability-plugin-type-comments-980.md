# Review — `capability` comments name plugin type (ut-docs#980)

- **Date:** 2026-10-06 · **Lane:** `lane:cloud-54` · **Branch:** `fix/980-capability-plugin-type-comments`
- **Author:** Opus 5.5 · **Independent reviewer:** Fable (one round, reviewed with the ut-cloud half)

## What shipped (comment-only)

- `specs/009-cloud-marketplace/contracts/marketplace.proto`:
  `ListPluginsRequest.capability`'s comment now matches ut-cloud's
  `cloud.proto` 0.0.8 — plugin type (ADR-0002, `internal/plugins.CanonicalTypes`),
  not a device/runtime capability.
- `internal/pages/plugin_api.go`: the `/api/plugins/marketplace` handler's
  verbatim forward of `?capability=` gets the same clarifying comment
  (#852 covered only `client.go`).

The behavioural half (portal filter, OpenAPI enum, taxonomy mirror guard
against this repo's `CanonicalTypes`) lives in ut-cloud —
`docs/code-reviews/2026-10-06-capability-plugin-type-980.md` there.

## Findings

None on this half (reviewer: "comment-only and builds").

## Verified

`gofmt -l` clean, `go build ./...`, `go vet ./internal/pages/`,
`go test ./internal/pages/` (179s, ok). All 42 guards in `ci.yml`'s build
job run: 40 pass; `guard-deadcode-baseline.sh` (local deadcode binary built
with go1.26 < go.mod 1.27) and `guard-shellcheck-version.sh` (no shellcheck
in this container) fail for environment reasons only — no Go or shell code
changed. No UI, locale or help-topic change.

## Verdict

Safe to merge once CI is green.
