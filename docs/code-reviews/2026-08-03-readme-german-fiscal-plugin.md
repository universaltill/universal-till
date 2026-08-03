# Code review — README German fiscal-plugin status (2026-08-03)

## Scope

Correct the core README entry for German fiscal compliance tracked by
`universaltill/ut-docs#286`. The README said that no code existed, despite the
separate `ut-plugin-tax-de` repository containing an explicitly incomplete
TSE/DSFinV-K skeleton.

## Change reviewed

- Replaces the stale zero-code claim with a public link to
  `ut-plugin-tax-de`.
- States the plugin's documented limitations: no real fiskaly-account
  verification, no legal validation, and no merchant use until the unresolved
  cloud-versus-hardware TSE decision in `ut-docs#38` is approved.

## Independent review

An independent read-only review checked the exact README diff against the
plugin README, `ut-docs` ADR-0025, and the core repository rules.

Initial finding: **medium** — the first wording could imply that a TSE path was
already approved. Fixed before this record by stating that the
cloud-versus-hardware decision remains unresolved and linking directly to the
open issue. No remaining findings.

## Verification

- TDD evidence: before the edit, a shell assertion requiring the removal of
  `zero code exists` failed with `FAIL: stale zero-code claim remains`; after
  the edit, the assertion passed along with checks for the plugin link,
  legal-validation caveat, and unresolved-decision wording.
- `git diff --check`
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `bash scripts/ci/guard-data-access.sh`
- `bash scripts/ci/guard-i18n.sh`

## Deferred

The actual TSE architecture, real fiskaly verification, and legal/compliance
validation remain deliberately blocked by `ut-docs#38`; this documentation
change does not alter that scope.

## Verdict

**Safe to merge.** The README now reflects the existing plugin without
overstating its readiness or compliance.
