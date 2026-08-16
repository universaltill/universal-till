# Code review: catalog_repo.go stale ADR-0039 citation → ADR-0051 (ut-docs#764)

**Date:** 2026-08-16
**Author (build):** Sonnet (inline, complexity:easy)
**Reviewer:** fresh-context Sonnet subagent, independent (no prior context of the build)

## What changed

`internal/data/catalog_repo.go:389` — a live code comment on `ExportRow`
cited "ADR-0039's interchange-format naming" for its JSON tag choices. The
docs repo (ut-docs#596) renumbered that decision to **ADR-0051** after
discovering two unrelated ADRs both numbered 0039. This PR updates the
comment to cite the correct number.

```diff
-// (ut-docs#600 review finding F1) match ADR-0039's interchange-format
+// (ut-docs#600 review finding F1) match ADR-0051's interchange-format
```

One line, comment-only, no behavior change.

## Sweep for other citations

Grepped for `ADR-0039` across every repo this session can reach
(`universal-till`, `ut-cloud`, `ut-infra`):

- `universal-till`: exactly one live-code hit — the line fixed above.
  Three more hits are all in `docs/code-reviews/*.md` — point-in-time
  historical review records that pre-date the renumbering (per the
  issue's own non-goal, these are left as-is, matching the pattern of
  the issue's cited example `2026-08-13-export-items-entity-600.md`).
- `ut-cloud`, `ut-infra`: no hits at all.
- `ut-docs` itself: explicitly out of scope (already fixed by ut-docs#596).

## Verification

- `go build ./...` — clean.
- `go vet ./internal/data/...` — clean.
- `go test ./...` (full suite) — all packages pass.
- `gofmt -l internal/data/catalog_repo.go` — no diff.
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all green (unaffected by a comment-only
  change, run anyway per the standing pre-commit gate).

## Independent review

A fresh-context Sonnet subagent (no visibility into how the change was
produced) independently re-derived the diff, re-ran the grep sweep across
all three reachable repos, and confirmed:

- The change matches the acceptance criteria exactly (right line, right
  text, comment still reads coherently).
- No other live-code citation of the interchange-format ADR was missed.
- Comment-only; no build/test/gofmt impact expected or observed.

**Verdict: PASS, no findings.**

## Scope notes

- Non-goal (per the issue): rewriting the three historical
  `docs/code-reviews/*.md` records that still say ADR-0039 — those are
  point-in-time records of what was true when written, not live
  citations, and are left untouched.
- Non-goal: anything in `ut-docs` itself — already corrected by ut-docs#596.

Closes universaltill/ut-docs#764.
