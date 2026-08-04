# Review — canonical logo refresh

**Scope:** `universal-till` portion of pipeline card #290, 2026-08-04.

## What changed

- Replaced visible legacy logo/wordmark assets with the supplied
  `unitill-logo.svg` mark and regenerated the packaged favicon.
- Updated login, setup, self-order, navigation, Android launcher references,
  and the macOS packaging source path. The navigation adds a light surface so
  the navy portion of the supplied mark remains visible on the dark header.
- Removed obsolete visible wordmark SVG assets and added a CI-enforced guard
  against restoring legacy template references.
- Added a Playwright assertion in the suite that CI actually executes.

## Review findings

An independent review found two medium issues in the first draft:

1. The updated logo assertion was in a legacy e2e directory not executed by
   CI. The assertion was added to `tests/e2e/tests/pos_ui_mvp.spec.ts`.
2. The new brand guard was local-only. It is now a named step in `ci.yml`.

Both fixes received a second independent review. No remaining findings.

## Verification

- TDD evidence: the asset guards failed before the canonical assets and
  template migration existed, then passed afterward.
- `go build ./...`, `go vet ./...`, `go test ./...`, the data-access and i18n
  guards, and the new brand guard passed.
- Playwright: 16/16 tests passed against a fresh `UT_AUTH=off` local server;
  this includes the canonical-logo accessibility assertion.
- The canonical SVG rasterizes successfully; its macOS `qlmanage` output and
  Android launcher dimensions were checked. No local server remains running.

**Verdict:** safe to merge.
