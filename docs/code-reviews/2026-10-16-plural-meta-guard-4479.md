# Review: plural metadata tied to a real ICU plural argument (ut-docs#4479)

Date: 2026-10-16 · Branch: `feat/4479-plural-meta-guard` · Lane: `lane:cloud-24`
Built by Sonnet (card `complexity:easy`); reviewed independently by Opus 5.5 (fresh context).

## What shipped

- `internal/msgfmt/plural_meta_test.go` (test-only; `msgfmt.go` is unchanged):
  `pluralArgs(m)` returns every plural argument name at any nesting depth
  (plural/select branches, exact `=N` branches), sorted and de-duplicated;
  `pluralMetaProblems` plus
  `TestPluralMetaProblems` (12 table cases, exact messages) and
  `TestRealLocaleBundle_PluralMetadataMatchesEnglish` over the real
  `web/locales/en.json` + `web/i18n-meta/en.meta.json`. `TestPluralArgs` stays in
  `msgfmt_test.go`. Both directions:
  - a meta `plural: X` needs an English value that parses with msgfmt and
    has `{X, plural, …}` (a `select` doesn't count);
  - an English value with a plural argument needs a meta entry whose
    `plural` names one of them.
- `scripts/ci/guard-i18n-meta.sh`: header comment for check 6 points at the
  Go test (comment only).

It's a Go test, not more Python in the guard, because the card requires the
value to parse with the parser the till actually formats with. It runs in
`ci.yml`'s `build` job (`go test $(go list ./... | grep -vE …)` includes
`internal/msgfmt`), the same job as `guard-i18n-meta.sh`.

**CI round 1 (`desktop-shell`, red):** the first push added an exported
`Message.PluralArgs` with no production caller, and `guard-deadcode-baseline.sh`
rejected it as unreachable. The rule is right: the walker exists only for a
check. It moved into the msgfmt package's own tests, where it can read the
unexported parse tree, so no production code is added. The plural-meta tests
moved with it, from `internal/config/i18n_tm_test.go` to the msgfmt package.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | The real-bundle test checks nothing today (no plural keys yet) | Accepted: the rule lands before the first plural key (#4480) |
| 2 | low | No self-test for a broken plural with no meta entry, or for an apostrophe-quoted plural (literal text) | Fixed: both cases added |
| 3 | nit | A non-string `plural` reads as "no `plural`" | Accepted: the shell guard's check 6 rejects a non-string `plural` in the same job |
| 4 | nit | `pluralArg` regex near-duplicates `icuArg` | Accepted: clearer as its own name |
| 5 | nit | `collectPluralArgs` sorted branch keys into a set that's sorted anyway | Fixed: sort removed |
| 6 | nit | Guard comment says `go test ./...`; CI runs a filtered list that includes `internal/msgfmt` | Accepted: the claim holds |
| 7 | nit | The no-meta-entry hint didn't mention description/review/baseline | Fixed: hint extended |

No blocker, high or medium findings. The reviewer confirmed:
- `PluralArgs` covers every nesting shape the parser produces.
- The regex matches a superset of what the parser accepts, so it can't hide a plural.
- The test's relative path resolves.
- Output is deterministic (sorted).

## Verified beyond the automated tests

- TDD: the tests were written first and failed with `m.PluralArgs undefined` /
  `undefined: pluralMetaProblems` (Dev).
- Re-verified by the orchestrator: with `PluralArgs` stubbed to return nothing,
  5 `TestPluralArgs` cases and 5 `TestPluralMetaProblems` cases fail. After
  restoring it, both packages pass. The same check ran again after the
  test-only move: with the `pluralArgs` walk disabled, 10 cases fail; restored,
  the package passes.
- Reviewer mutations:
  - dropping the "no meta entry" branch fails the test;
  - narrowing the prefilter to declared keys fails 2 cases.
- Gate: `gofmt -l` is clean. `go vet` and `golangci-lint` report 0 issues for the
  touched packages. `go build ./...` passes. `go test` passes for msgfmt and config.
  `guard-i18n-meta.sh` passes and its `_test.sh` self-test passes.
- Not covered locally:
  - The local `go test ./...` timed out in `internal/plugins` under the default
    10-minute limit. Run alone, it passes in 769s. That package has its own CI
    step (ut-docs#643).
  - shellcheck isn't installed here. The script change is comment-only.
- No UI surface, locale keys or manual topics are touched.

## Verdict

Safe to merge.
