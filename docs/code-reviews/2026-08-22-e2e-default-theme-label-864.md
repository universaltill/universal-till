# Code review: honest "default theme" label in contrast e2e specs (ut-docs#864)

**Date:** 2026-08-22
**Card:** universaltill/ut-docs#864
**Author:** Universal Till autonomous pipeline (Farshid Mirza, `Co-Authored-By: Claude`)
**Reviewer:** independent fresh-context subagent (Sonnet), per `complexity:easy` routing

## Summary

`e2e/tests/form-input-contrast-305.spec.ts` and
`e2e/tests/focus-border-contrast-797.spec.ts` both iterate a `THEMES` array
whose first entry, `'default'`, is a control-flow key meaning "skip
injecting an extra `/themes/<name>.css` stylesheet" — not a real
selectable theme (there is no `web/public/themes/default.css`). The till
these specs drive boots via `e2e/run-till.sh`, which never sets
`UT_THEME`; `internal/config/config.go`'s `getenv("UT_THEME", "monarch")`
means the server actually boots with **monarch** live. So every
`"default theme: ..."` sub-test title was silently measuring monarch's
CSS while claiming to measure a genuine bare-`app.css` state.

Confirmed harmless for #305 (its `--control-border` check is uniformly
≥3:1 across every curated theme including monarch) but confirmed to have
actually caused a misleading result for #797 (the mislabeled sub-test
failed with monarch's raw accent colour `2.54:1`, not the genuinely
bare-default `#2563eb`/`5.17:1` the spec's own comments describe).

## Fix

Chose the relabel option (the issue's own AC offered either relabeling or
making the till boot genuinely themeless for that probe). Making the boot
genuinely themeless would require either a third Playwright project/server
just for one probe, or changing `run-till.sh`'s env for the *whole* shared
server all other specs in the `default` project also drive — both riskier
and more invasive than the actual defect warrants for an `easy`-complexity
card, whereas relabeling is zero-risk and satisfies the AC directly.

Added a `THEME_LABELS: Record<(typeof THEMES)[number], string>` map in
each spec file mapping `'default' → 'server-default (monarch)'` (identity
for the other four), and switched every test title and `expect(...)`
failure message to use `THEME_LABELS[theme]` instead of the bare `theme`
key. The `'default'` string itself is left completely untouched as the
control-flow key (`if (theme !== 'default')`) and in the
`/themes/${theme}.css` URL construction for the other four themes — no
behavioral change, pure relabeling.

## What was verified

- `git diff` is a clean, additive-only change to exactly the two spec
  files (no other file touched).
- Ran the full 20-test suite for both specs for real, against a real
  built `universal-till` binary and real Chromium
  (`npx playwright test tests/form-input-contrast-305.spec.ts
  tests/focus-border-contrast-797.spec.ts --project=default`, browser
  pointed at this sandbox's pre-installed Chromium via a throwaway,
  untracked config copy — not committed, deleted after the run). All 20
  passed; the `'default'` sub-tests now read
  `server-default (monarch) theme: ...` in their titles, and none of the
  amber/fresh/monarch/slate results changed.
- Independent fresh-context review confirmed: `THEME_LABELS` is
  exhaustive/type-safe (TS forces every `THEMES` entry to have a label),
  no stray bare `${theme}` remains in any title/message string, the two
  intentionally-untouched `theme` usages (control-flow check, stylesheet
  URL) are correctly left alone, and the two factual claims in the new
  code comments (`UT_THEME` defaults to `"monarch"` in
  `internal/config/config.go:97`; `run-till.sh` never sets it) were
  checked against the actual source and are accurate.
- No CLAUDE.md guard applies — test-only file under `e2e/tests/`, no Go,
  no money, no user-facing template strings, no data access.
- `gofmt -l .` silent (no `.go` files touched); diff stat confirms only
  the two intended files changed.

## Outcome

Ship as-is. No blocking issues found by the independent review; two
non-blocking nits noted (label verbosity, and that the "make it genuinely
themeless" AC branch wasn't pursued) — both judged acceptable, documented
above.
