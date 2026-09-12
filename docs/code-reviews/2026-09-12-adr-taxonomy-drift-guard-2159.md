# Cross-ADR plugin-taxonomy count drift guard (ut-docs#2159)

**Date:** 2026-09-12
**Card:** ut-docs#2159, split out of ut-docs#2150's ADR doc-vs-code drift audit.
**Builder:** Sonnet (this cycle, `lane:cloud-54`)
**Reviewer:** Opus subagent, fresh context, independent of the build
**Paired PR:** `universaltill/ut-docs` — 4 ADR wording fixes + 2 historical-count
markers (must merge first; see that repo's own
`code-reviews/2026-09-12-adr-taxonomy-cross-adr-guard-2159.md` for the full
cross-repo record, findings, and reviewer verification — this file covers
this repo's own diff specifically).

## What changed in this repo

- New `scripts/ci/guard-adr-taxonomy-drift.sh`: scans every `adr/*.md` file in
  a sibling `ut-docs` checkout (except `0002-*.md`, which
  `guard-adr-plugin-taxonomy.sh` already owns exclusively) for a plugin-
  taxonomy count restated in one of 4 known current-fact phrasings, failing
  if the number disagrees with the live `CanonicalTypes` count. A line
  carrying `<!-- taxonomy-count:historical -->` is exempt (mirrors this
  repo's own `i18n:ignore`/`compliance-claim:allow` convention).
- New `scripts/ci/guard-adr-taxonomy-drift_test.sh`: fixture-based regression
  test, same style as the sibling `guard-adr-plugin-taxonomy_test.sh`.
- `.github/workflows/ci.yml`: wired both into the existing `adr-taxonomy-guard`
  job (same `ut-docs` checkout, same `DOCS_READ_TOKEN`-gated skip-loudly
  convention, self-test-before-real-run ordering).
- `CLAUDE.md`: documented the new guard alongside the existing one.

## Verification run in this session (all commands actually executed)

- `gofmt -l .` — clean (no `.go` files touched).
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` — 0 issues.
- `go test ./...` — full suite green, 58 packages, 0 failures (this change
  touches no Go code, so this run confirms the change has zero effect on the
  build — not that it needed new coverage there).
- `bash scripts/ci/guard-adr-taxonomy-drift_test.sh` — 10/10 fixture cases
  pass (after the review-driven fixes below).
- `bash scripts/ci/guard-adr-taxonomy-drift.sh` (against the real, fixed
  sibling `ut-docs` checkout) — passes.
- `bash scripts/ci/guard-adr-plugin-taxonomy.sh` + its own `_test.sh` — both
  still green, untouched by this change.
- `python3 -c "import yaml; yaml.safe_load(...)"` — `.github/workflows/ci.yml`
  parses as valid YAML after the edit.
- `shellcheck` is **not installed in this sandbox** — could not run the repo's
  own `shellcheck scripts/ci/*.sh` standard against the two new scripts.
  Manual read found nothing shellcheck-shaped (every expansion quoted,
  `find -print0`/`IFS=: read -r` used correctly, `trap 'rm -rf "${TMPDIR}"'`
  targets a variable not a bare glob). Flagging so a session with shellcheck
  available runs it before this merges, per the repo standard.

## Independent review findings, fixed before this commit

Full detail in the paired `ut-docs` PR's review record (same findings, one
record since the review covered both repos together). Summary of what
changed here as a result:

1. Added a 4th detection pattern (`\([0-9]+ types[;,)]`) — the guard's first
   draft did not match ADR-0088's own "(22 types;" phrasing at all, so a
   future 23rd-type addition would have gone unnoticed by the exact guard
   built to catch that.
2. Added fixture case 5b (paired with the existing 5) so the fixture suite
   actually exercises the new pattern's fail path, not just its pass path —
   5 alone couldn't distinguish "correctly matched and compared" from "never
   matched at all," which is exactly how gap #1 went unnoticed on the first
   pass.
3. `grep` → `grep -a` on the file-scanning call, closing a theoretical
   silent-skip path if grep ever classified an ADR file as binary.
4. `CLAUDE.md`'s description of the new guard's coverage reworded to point at
   the guard's own header (which lists exactly which phrasings it matches and
   its known gaps) rather than an unconditional "every ADR" claim that could
   itself drift from the code.

Re-ran the fixture suite, the real-repo check, and the sibling guard after
each fix — all green (see the verification list above, which reflects the
final, post-fix state).

## Outcome

Diff limited to the 2 new script files, the CI workflow step additions, and
the `CLAUDE.md` doc update. No Go code touched. No ADR contradicted; this is
tooling/CI, not an architectural decision, so no ADR was written for it
(confirmed at the Architect step). Ordering dependency: this PR's own next CI
run reads `ut-docs`'s current `main` for the guard's real-repo check to pass —
merge the paired `ut-docs` PR first, same dependency `CLAUDE.md` already
documents for a `CanonicalTypes` change.
