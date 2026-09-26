# Review — core-neutral guard (ut-docs#2888)

**Change:** `scripts/ci/coreneutral` (go/parser + go/ast, stdlib only) behind
`scripts/ci/guard-core-neutral.sh`, wired into ci.yml's build job (with a depth-1
fetch of `origin/main` for the shrink check). Flags, in non-test Go under
`internal/`, `cmd/`, `mobile/`: country codes from a curated list in testing
positions (==/!=, case values, literal switch tags, EqualFold/HasPrefix/…/
slices.Contains args, literal map indexes, set-map keys, and consts/vars bound
to such literals), vendor names as tokens inside string literals (incl. struct
tags), and plugin-id literals. Allow-list `scripts/ci/core-neutral-allowlist.txt`
keyed `path|func|"literal" # #card reason` (26 entries, each citing a move card
from the #2850 audit or #2850 "class a"); stale entries fail; **shrink-only**:
entries absent from `origin/main`'s list fail (active from the first merge).
Inline `// core-neutral:allow <reason>` (real comment, non-empty reason) for
genuinely neutral sites (one: enroll.go ADR-0049 hosting-region routing).
CLAUDE.md: 3-line rule. Pfandrückgabe (card item 2): **no change** — the value
is a stored cash-adjustment reason real rows carry and reports group by;
renaming strands history, and it's not a country/vendor/plugin-id literal.

Authors: Sonnet (awk v1), Opus 5.5 (go/ast rewrite + fixes).
Reviewers: Opus 5.5 (v1), Fable (v2 re-review).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker (v1) | awk comment stripper didn't know string literals: `"/static/*"` or any URL `//` blinded the guard | rewritten on go/ast; all 13 reviewer probes caught |
| 2 | major (v1) | false negatives: middle case values, slices.Contains, HasPrefix, lowercase, raw strings, `de_DE`, struct tags | all flagged in v2 (table tests) |
| 3 | major (v1) | shrink-only by convention; empty reasons; line-number keys | base-branch comparison enforced; reason required; keys are path\|func\|literal |
| 4 | major (v2) | consts missed — incl. the real `setup_tse.go` `tseProvisionCountry = "DE"` gate | consts/unreassigned vars resolved, keyed at the decl; entry added citing #2881 |
| 5 | major (v2) | lowercase bare codes = noise (8 false-positive markers) | lowercase counts only against country-like names; 8 markers removed, files back to main |
| 6 | minor | named map types, `mobile/` unscanned, same-func second literal | resolved / scanned / documented |
| 7 | minor | `enroll.go` DE region tag | neutral (ADR-0049 routing) → inline allow; only-DE→EU mapping looks wrong → follow-up card |
| 8 | minor | #2879 body didn't list 4 allow-listed sites | card amended |

**Accepted limits (documented):** Sprintf/concat-built codes, aliased tables,
`loc == "tr"` where the variable isn't country-named.

**Verification:** `go test ./scripts/ci/coreneutral/` (81 cases), guard on the
real tree (436 files, 26 entries, clean), wrapper self-test (temp git repo for
stale + shrink), shellcheck, gofmt, go vet; against an export of origin/main the
merge commit passes. CI cost: ~2.3 s cold, ~0.25 s warm.

**Verdict:** safe to merge.
